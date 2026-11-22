package extdb

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// StoredDims is how many floats ext_vectors stores per vector: a model's
// own dimensions first, then zeros.
const StoredDims = 4096

// Mechanism is the vector_mechanism of the vectors the Studio writes: one
// per chunk of text, as read.
const Mechanism = "semantic"

// Embedder makes vectors with gufi_sqlite3, which carries sqlite-vec and
// sqlite-lembed: a GGUF model's vector of each chunk, made unit length.
type Embedder struct {
	// GufiSqlite3 is the gufi_sqlite3 binary.
	GufiSqlite3 string
	// Model is the GGUF model file, ModelName the name its vectors carry
	// (vector_model_name; a query's model list names it the same) and
	// Dims its dimensions.
	Model, ModelName string
	Dims             int
	// MaxChars is the most characters of a chunk that are embedded: past
	// the model's context the rest is not seen anyway.
	MaxChars int
}

// DefaultModelName is the name of the vectors of the Studio's GGUF model
// (all-MiniLM-L6-v2 at q8_0), as its source names it.
const DefaultModelName = "sentence-transformers/all-MiniLM-L6-v2"

func (e Embedder) check() error {
	if e.GufiSqlite3 == "" || e.Model == "" || e.ModelName == "" || e.Dims <= 0 || e.Dims > StoredDims {
		return errors.New("extdb: an embedder needs gufi_sqlite3, a model, its name and its dimensions")
	}
	return nil
}

// run runs SQL strings in one gufi_sqlite3 on db, each as one exec.
func runSqlite3(ctx context.Context, bin, db string, stmts ...string) error {
	cmd := exec.CommandContext(ctx, bin, append([]string{db}, stmts...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	msg := strings.TrimSpace(stderr.String())
	if err == nil && !strings.Contains(msg, "Error") {
		return nil
	}
	if len(msg) > 400 {
		msg = msg[len(msg)-400:]
	}
	if msg == "" && err != nil {
		msg = err.Error()
	}
	return fmt.Errorf("gufi_sqlite3: %s", msg)
}

// EmbedStats is what embedding an artifact did.
type EmbedStats struct {
	Chunks int `json:"chunks"`
}

// EmbedArtifact gives each chunk of the artifact that has no vector of
// this model one, in studio_vectors: unit length at the model's own
// dimensions. The files it embedded are marked as recorded now, so a
// LoadSpreadSince of this run carries their vectors; each asset's
// indexing_provenance names the model.
func (e Embedder) EmbedArtifact(ctx context.Context, artifact string) (EmbedStats, error) {
	var st EmbedStats
	if err := e.check(); err != nil {
		return st, err
	}
	a, err := Create(artifact) // brings its schema up to date
	if err != nil {
		return st, err
	}
	a.Close()
	max := e.MaxChars
	if max <= 0 {
		max = 800
	}
	name, mech := quote(e.ModelName), quote(Mechanism)
	prov := fmt.Sprintf("json_object('model_name', %s, 'native_dimensions', %d, 'stored_dimensions', %d, 'normalized', json('true'), 'distance_metric', 'cosine', 'embedded_chars', %d, 'model_file', %s)",
		name, e.Dims, StoredDims, max, quote(e.Model))
	err = runSqlite3(ctx, e.GufiSqlite3, artifact,
		"INSERT INTO temp.lembed_models (name, model) SELECT 'm', lembed_model_from_file("+quote(e.Model)+");",
		"CREATE TEMP TABLE todo AS SELECT b.asset_id AS asset_id, b.outer_file_uuid AS outer_file_uuid, b.chunk_sequence AS chunk_id, b.content AS content FROM ext_bm25 b "+
			"WHERE NOT EXISTS (SELECT 1 FROM studio_vectors v WHERE v.asset_id = b.asset_id AND v.chunk_id = b.chunk_sequence AND v.vector_model_name = "+name+" AND v.vector_mechanism = "+mech+");",
		"BEGIN; "+
			"INSERT OR REPLACE INTO studio_vectors ("+vecRowCols+") SELECT asset_id, outer_file_uuid, chunk_id, "+mech+", "+name+", "+fmt.Sprint(e.Dims)+", "+
			fmt.Sprintf("vec_normalize(lembed('m', substr(content, 1, %d))) FROM temp.todo; ", max)+
			"UPDATE ext_assets SET indexing_provenance = json_set(coalesce(indexing_provenance, '{}'), '$.semantic', "+prov+") WHERE asset_id IN (SELECT asset_id FROM temp.todo); "+
			"UPDATE studio_extract_files SET at = unixepoch() WHERE EXISTS (SELECT 1 FROM ext_assets x WHERE x.asset_id IN (SELECT asset_id FROM temp.todo) "+
			"AND x.inode = studio_extract_files.inode AND x.mtime = studio_extract_files.mtime AND x.extra_info IS studio_extract_files.extra_info); "+
			"COMMIT;",
	)
	if err != nil {
		return st, err
	}
	db, err := Create(artifact)
	if err != nil {
		return st, err
	}
	defer db.Close()
	db.db.QueryRow("SELECT count(*) FROM studio_vectors WHERE vector_model_name = ?", e.ModelName).Scan(&st.Chunks)
	return st, nil
}

// VecShards makes ext_vectors in shards written with their vectors in
// studio_vectors (FillShardsWith), and drops studio_vectors, leaving each
// with GUFI's schema alone. bin is gufi_sqlite3.
func VecShards(ctx context.Context, bin string, shards []string) error {
	if len(shards) == 0 {
		return nil
	}
	var stmts []string
	for _, f := range shards {
		// Each shard is attached as d; the schema's statements name it.
		schema := strings.Replace(VectorsSchema, "CREATE VIRTUAL TABLE IF NOT EXISTS ext_vectors ", "CREATE VIRTUAL TABLE IF NOT EXISTS d.ext_vectors ", 1)
		schema = strings.Replace(schema, "CREATE VIEW IF NOT EXISTS ext_vectors_v ", "CREATE VIEW IF NOT EXISTS d.ext_vectors_v ", 1)
		stmts = append(stmts,
			"ATTACH "+quote(f)+" AS d;",
			"BEGIN; "+schema+
				" CREATE TEMP TABLE c AS SELECT asset_id, chunk_sequence, page_start, content FROM d.ext_bm25;"+
				" CREATE INDEX temp.c_a ON c (asset_id, chunk_sequence);"+
				" INSERT INTO d.ext_vectors (inode, mtime, extra_info, outer_file_uuid, asset_id, embedding_id, chunk_id, vector_mechanism, vector_model_name,"+
				" vector_native_dimensions, vector_stored_dimensions, page_number, chunk_content, embedding)"+
				" SELECT a.inode, a.mtime, a.extra_info, v.outer_file_uuid, v.asset_id, row_number() OVER (ORDER BY v.asset_id, v.chunk_id), v.chunk_id,"+
				fmt.Sprintf(" v.vector_mechanism, v.vector_model_name, v.vector_native_dimensions, %d, c.page_start, c.content,", StoredDims)+
				// || makes text of blobs; hex and unhex keep the bytes.
				fmt.Sprintf(" vec_f32(unhex(hex(v.embedding) || hex(zeroblob(4 * (%d - v.vector_native_dimensions)))))", StoredDims)+
				" FROM d.studio_vectors v JOIN d.ext_assets a ON a.asset_id = v.asset_id AND a.outer_file_uuid = v.outer_file_uuid"+
				" JOIN temp.c ON c.asset_id = v.asset_id AND c.chunk_sequence = v.chunk_id;"+
				" DROP TABLE d.studio_vectors; DROP TABLE temp.c; COMMIT;",
			// studio_vectors' pages are given back.
			"VACUUM d;",
			"DETACH d;",
		)
	}
	return runSqlite3(ctx, bin, ":memory:", stmts...)
}
