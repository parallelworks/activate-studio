// Package embed is the Studio's sentence embedder in Go: all-MiniLM-L6-v2,
// int8, mean pooling, through ONNX Runtime loaded at run time (package
// ort) with the tokenizer in package wordpiece. It stands in for
// indexer/embed_onnx.py and the two programs that use it, embed.py (the
// index's "meaning" phase: Pass) and embed_query.py (the server's query
// vectors: Queries), with the same model files fetch_embed.py puts in
// INDEX_BASE/models/minilm-l6 and the same vectors within float32
// rounding.
//
// Vectors are 384 float32 values, L2-normalized, little-endian: the blob
// layout of sqlite-vec's float[384] columns.
//
// The API other packages use:
//
//	embed.ModelID, embed.ModelIDFor(maxTokens), embed.IsONNXModel(id)
//	embed.ModelDir(indexBase)               INDEX_BASE/models/minilm-l6
//	embed.Unavailable(modelDir) string      why the backend cannot run, "" when it can
//	embed.New(modelDir, Options) (*Embedder, error)
//	(*Embedder).Embed(texts) ([][]byte, error)   one blob per text, in order
//	(&embed.Queries{Dir: modelDir}).Embed(texts)   query vectors from one
//	                                        `embed-query` child, started on first
//	                                        use, stopped when idle (embedWorker.ts)
//	embed.Pass(ctx, PassOptions) (PassResult, error)   embed.py over an index tree
//
// The ONNX Runtime library is found by FindLibrary.
package embed

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/parallelworks/activate-studio/server-go/internal/embed/ort"
	"github.com/parallelworks/activate-studio/server-go/internal/embed/wordpiece"
)

const (
	// ModelID marks vectors of this model at the default 256 tokens.
	ModelID = "minilm-l6-mean-int8"
	// MaxTokens is the default cut of a passage, [CLS] and [SEP] included.
	MaxTokens = 256
	// Dims is the vector length.
	Dims = 384
	// Batch is how many texts go through the model at once (embed_onnx.py:
	// as fast as 32, with about 60 percent of the memory).
	Batch = 16
)

// ModelIDFor is the id an index's vectors are marked with for a cut of
// maxTokens tokens: ModelID, or ModelID-t128.
func ModelIDFor(maxTokens int) string {
	if maxTokens == MaxTokens || maxTokens == 0 {
		return ModelID
	}
	return fmt.Sprintf("%s-t%d", ModelID, maxTokens)
}

// IsONNXModel reports whether vectors marked id come from this model, at
// any cut.
func IsONNXModel(id string) bool { return id == ModelID || strings.HasPrefix(id, ModelID+"-t") }

// ModelDir is where the model files are under INDEX_BASE.
func ModelDir(indexBase string) string { return filepath.Join(indexBase, "models", "minilm-l6") }

// ModelPresent reports whether dir holds model.onnx and tokenizer.json.
func ModelPresent(dir string) bool {
	return isFile(filepath.Join(dir, "model.onnx")) && isFile(filepath.Join(dir, "tokenizer.json"))
}

// Unavailable says why the backend cannot run with the model in dir, or
// "" when it can: the model files, and a loadable ONNX Runtime.
func Unavailable(dir string) string {
	if dir == "" || !ModelPresent(dir) {
		return fmt.Sprintf("no ONNX model in %s", dir)
	}
	lib, err := FindLibrary()
	if err != nil {
		return err.Error()
	}
	if _, err := ort.Load(lib); err != nil {
		return err.Error()
	}
	return ""
}

// Options configure an Embedder.
type Options struct {
	// Threads is ONNX Runtime's intra-op thread count per session (1 when 0).
	Threads int
	// MaxTokens cuts each text, [CLS] and [SEP] included (MaxTokens when 0).
	MaxTokens int
	// Library is the ONNX Runtime library; FindLibrary() when empty.
	Library string
}

// Embedder is one loaded model. Embed is safe for concurrent use; calls
// share the one session, which ONNX Runtime allows.
type Embedder struct {
	tok       *wordpiece.Tokenizer
	sess      *ort.Session
	maxTokens int
	typeIDs   bool
	output    string
}

var tokenizers sync.Map // path -> *wordpiece.Tokenizer

func loadTokenizer(path string) (*wordpiece.Tokenizer, error) {
	if t, ok := tokenizers.Load(path); ok {
		return t.(*wordpiece.Tokenizer), nil
	}
	t, err := wordpiece.Load(path)
	if err != nil {
		return nil, err
	}
	tokenizers.Store(path, t)
	return t, nil
}

// New loads the model in dir.
func New(dir string, o Options) (*Embedder, error) {
	if !ModelPresent(dir) {
		return nil, fmt.Errorf("no ONNX model in %s", dir)
	}
	lib := o.Library
	if lib == "" {
		var err error
		if lib, err = FindLibrary(); err != nil {
			return nil, err
		}
	}
	rt, err := ort.Load(lib)
	if err != nil {
		return nil, err
	}
	tok, err := loadTokenizer(filepath.Join(dir, "tokenizer.json"))
	if err != nil {
		return nil, err
	}
	sess, err := rt.NewSession(filepath.Join(dir, "model.onnx"), ort.SessionOptions{IntraOpThreads: max(o.Threads, 1), InterOpThreads: 1})
	if err != nil {
		return nil, err
	}
	e := &Embedder{tok: tok, sess: sess, maxTokens: o.MaxTokens}
	if e.maxTokens <= 0 {
		e.maxTokens = MaxTokens
	}
	for _, n := range sess.Inputs() {
		if n == "token_type_ids" {
			e.typeIDs = true
		}
	}
	if len(sess.Outputs()) == 0 {
		sess.Close()
		return nil, errors.New("the model has no outputs")
	}
	e.output = sess.Outputs()[0]
	return e, nil
}

// Close releases the model.
func (e *Embedder) Close() { e.sess.Close() }

// Embed returns one blob per text, in order. Texts of like length share
// a batch, so little of it is padding; an empty text is embedded as one
// space, as embed_onnx.py does.
func (e *Embedder) Embed(texts []string) ([][]byte, error) {
	order := make([]int, len(texts))
	for i := range order {
		order[i] = i
	}
	// Python's sort by len(text), stable: code points.
	lens := make([]int, len(texts))
	for i, t := range texts {
		lens[i] = utf8.RuneCountInString(t)
	}
	sort.SliceStable(order, func(a, b int) bool { return lens[order[a]] < lens[order[b]] })
	out := make([][]byte, len(texts))
	for start := 0; start < len(order); start += Batch {
		idx := order[start:min(start+Batch, len(order))]
		batch := make([]string, len(idx))
		for i, j := range idx {
			batch[i] = texts[j]
		}
		vecs, err := e.batch(batch)
		if err != nil {
			return nil, err
		}
		for i, j := range idx {
			out[j] = vecs[i]
		}
	}
	return out, nil
}

func (e *Embedder) batch(texts []string) ([][]byte, error) {
	ids := make([][]int32, len(texts))
	width := 0
	for i, t := range texts {
		if t == "" {
			t = " "
		}
		ids[i] = e.tok.Encode(t, e.maxTokens)
		width = max(width, len(ids[i]))
	}
	n := len(texts)
	inputIDs := make([]int64, n*width)
	mask := make([]int64, n*width)
	pad := int64(e.tok.PadID())
	for i, row := range ids {
		for j := range width {
			if j < len(row) {
				inputIDs[i*width+j] = int64(row[j])
				mask[i*width+j] = 1
			} else {
				inputIDs[i*width+j] = pad
			}
		}
	}
	shape := []int64{int64(n), int64(width)}
	in := []ort.Int64Input{{Name: "input_ids", Shape: shape, Data: inputIDs}, {Name: "attention_mask", Shape: shape, Data: mask}}
	if e.typeIDs {
		in = append(in, ort.Int64Input{Name: "token_type_ids", Shape: shape, Data: make([]int64, n*width)})
	}
	hidden, dims, err := e.sess.RunFloat(in, e.output)
	if err != nil {
		return nil, err
	}
	if len(dims) != 3 || dims[0] != int64(n) || dims[1] != int64(width) {
		return nil, fmt.Errorf("the model answered with shape %v for %d texts of %d tokens", dims, n, width)
	}
	d := int(dims[2])
	out := make([][]byte, n)
	v := make([]float32, d)
	for i := range n {
		pool(v, hidden[i*width*d:(i+1)*width*d], mask[i*width:(i+1)*width], d)
		out[i] = Blob(v)
	}
	return out, nil
}

// pool is mean pooling over the tokens the mask keeps, then L2
// normalization, in float32 as numpy computes it.
func pool(v, hidden []float32, mask []int64, d int) {
	for k := range v {
		v[k] = 0
	}
	var count float32
	for t, m := range mask {
		if m == 0 {
			continue
		}
		count++
		row := hidden[t*d : (t+1)*d]
		for k := range v {
			v[k] += row[k]
		}
	}
	count = max(count, 1e-9)
	var ss float32
	for k := range v {
		v[k] /= count
		ss += v[k] * v[k]
	}
	norm := max(float32(math.Sqrt(float64(ss))), 1e-12)
	for k := range v {
		v[k] /= norm
	}
}

// Blob is v as little-endian float32 bytes.
func Blob(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

// Floats is a blob as float32 values.
func Floats(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}
