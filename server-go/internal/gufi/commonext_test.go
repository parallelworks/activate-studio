package gufi

import (
	"context"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// floatsHex is n float32 values, little endian, with the first set to 1:
// a unit vector, as hex.
func floatsHex(n int) string {
	b := make([]byte, 4*n)
	copy(b, []byte{0x00, 0x00, 0x80, 0x3f})
	return hex.EncodeToString(b)
}

func externalCopies(t *testing.T, stages []string, pattern string) []string {
	t.Helper()
	var out []string
	for i, s := range stages {
		if s == "--external-copy" {
			if i+2 >= len(stages) || stages[i+1] != pattern {
				t.Fatalf("--external-copy at %d is not followed by %q and its SQL: %q", i, pattern, stages[i:])
			}
			out = append(out, stages[i+2])
		}
	}
	return out
}

func TestCommonExternalStages(t *testing.T) {
	ext := `\.db$`
	lim := WalkLimits{Names: 10, Paths: 10, Words: 60, WordsPerDir: 20, Vectors: 10, VectorsPerDir: 3, StudioText: true, External: &ext}
	vecs := []QueryVector{{Pattern: "%all-MiniLM-L6-v2", Hex: floatsHex(384)}}

	copies := externalCopies(t, OnePassStages("budget", lim, []string{"/ix"}, vecs, nil), ext)
	if len(copies) != 3 {
		t.Fatalf("want the earlier schema's copy and one each for ext_bm25 and ext_vectors, got %d: %q", len(copies), copies)
	}
	if !strings.Contains(copies[0], "extdb.ext_bm25_by_file") || !strings.Contains(copies[0], "extdb.ext_vectors_by_file") {
		t.Errorf("the earlier schema's copy changed: %s", copies[0])
	}
	for _, want := range []string{
		"FROM extdb.ext_bm25 JOIN vrpentries e ON CAST(ext_bm25.inode AS INT64) = e.inode AND CAST(ext_bm25.mtime AS INT64) = e.mtime",
		"WHERE ext_bm25 MATCH ",
		"LIMIT 20;",
	} {
		if !strings.Contains(copies[1], want) {
			t.Errorf("ext_bm25 copy lacks %q: %s", want, copies[1])
		}
	}
	for _, want := range []string{
		"FROM extdb.ext_vectors x JOIN qv ON x.vector_model_name LIKE qv.pat",
		"JOIN vrpentries e ON CAST(x.inode AS INT64) = e.inode AND CAST(x.mtime AS INT64) = e.mtime",
		"WHERE x.vector_native_dimensions * 4 = length(qv.qe)",
		"LIMIT 3;",
	} {
		if !strings.Contains(copies[2], want) {
			t.Errorf("ext_vectors copy lacks %q: %s", want, copies[2])
		}
	}

	// Words only: the common schema's text alone.
	if c := externalCopies(t, OnePassStages("budget", lim, []string{"/ix"}, nil, nil), ext); len(c) != 2 || !strings.Contains(c[1], "ext_bm25 MATCH") {
		t.Errorf("without vectors want the earlier copy and ext_bm25, got %q", c)
	}
	// No pattern: no external databases at all.
	lim.External = nil
	if c := externalCopies(t, OnePassStages("budget", lim, []string{"/ix"}, vecs, nil), ext); len(c) != 0 {
		t.Errorf("with no pattern want no copies, got %q", c)
	}
}

// TestCommonExternalWalk puts an ext_all.db in GUFI's common schema beside
// the db.db of the fixture index's notes folder and checks that the walk finds a file by
// its text there and, for a model whose query vector it has, by meaning;
// a vector of another model, or of other dimensions, is not compared.
func TestCommonExternalWalk(t *testing.T) {
	bin := gufiBin(t)
	root := buildFixture(t, bin, t.TempDir())
	local := NewLocal(bin)
	ctx := context.Background()
	dir := filepath.Join(root, "notes")
	res, err := local.Exec(ctx, Command{Tool: ToolSQLite3, Args: []string{"-d", "x", filepath.Join(dir, "db.db"), "SELECT inode, mtime, name FROM entries WHERE type = 'f' ORDER BY name LIMIT 2;"}})
	if err != nil {
		t.Fatal(err)
	}
	rows := ParseRows(string(res.Stdout), 3)
	if len(rows) < 2 {
		t.Fatalf("the fixture's notes folder has %d files", len(rows))
	}
	file, other := rows[0], rows[1]
	vec := func(r []string, model string, native int, first bool) string {
		v := strings.Repeat("00", 4*4096)
		if first {
			v = floatsHex(4096)
		}
		return fmt.Sprintf("INSERT INTO ext_vectors (inode, mtime, extra_info, outer_file_uuid, asset_id, embedding_id, chunk_id, vector_mechanism, vector_model_name, "+
			"vector_native_dimensions, vector_stored_dimensions, chunk_content, embedding) VALUES (%s, %s, '', 'u-%s', 'a-%s', 0, 0, 'semantic', %s, %d, 4096, %s, X'%s');",
			r[0], r[1], r[0], r[0], q(model), native, q("passage of "+r[2]), v)
	}
	sql := []string{
		"CREATE TABLE ext_assets (inode INTEGER NOT NULL, mtime INTEGER NOT NULL, extra_info TEXT, outer_file_uuid TEXT NOT NULL, asset_id TEXT PRIMARY KEY, " +
			"component_type TEXT NOT NULL, member_path_json TEXT NOT NULL, file_metadata TEXT, indexing_provenance TEXT, mimetype TEXT, asset_route TEXT);",
		"CREATE VIRTUAL TABLE ext_bm25 USING fts5(inode UNINDEXED, mtime UNINDEXED, extra_info UNINDEXED, outer_file_uuid UNINDEXED, asset_id UNINDEXED, " +
			"chunk_sequence UNINDEXED, start_position UNINDEXED, end_position UNINDEXED, page_start UNINDEXED, page_end UNINDEXED, audio_start_sec UNINDEXED, " +
			"video_start_sec UNINDEXED, video_end_sec UNINDEXED, content);",
		"CREATE VIRTUAL TABLE ext_vectors USING vec0(inode INTEGER, mtime INTEGER, extra_info TEXT, +outer_file_uuid TEXT, +asset_id TEXT, +embedding_id INTEGER, " +
			"+chunk_id INTEGER, vector_mechanism TEXT, vector_model_name TEXT, vector_native_dimensions INTEGER, vector_stored_dimensions INTEGER, " +
			"+page_number INTEGER, +audio_start_sec FLOAT, +video_start_sec FLOAT, +video_end_sec FLOAT, +chunk_content TEXT, +chunk_prefix TEXT, " +
			"+chunk_overlap TEXT, embedding FLOAT32[4096] distance_metric=L2);",
		fmt.Sprintf("INSERT INTO ext_bm25 VALUES (%s, %s, '', 'u-%s', 'a-%s', 0, 0, 30, 0, 0, NULL, NULL, NULL, 'quartzite deposits near zanzibar');", file[0], file[1], file[0], file[0]),
		// The text of a file as it was before its last change: not matched.
		fmt.Sprintf("INSERT INTO ext_bm25 VALUES (%s, %s, '', 'u-%s', 'a-%s', 0, 0, 30, 0, 0, NULL, NULL, NULL, 'stale zanzibar text');", other[0], other[1]+"1", other[0], other[0]),
		vec(file, "sentence-transformers/all-MiniLM-L6-v2", 384, true),
		vec(other, "multi-qa-mpnet-base-dot-v1", 768, true),
		vec(other, "sentence-transformers/all-MiniLM-L6-v2", 768, true),
	}
	res, err = local.Exec(ctx, Command{Tool: ToolSQLite3, Args: append([]string{filepath.Join(dir, "ext_all.db")}, sql...)})
	if err != nil || res.Stderr != "" {
		t.Fatalf("writing ext_all.db: %v %s", err, res.Stderr)
	}

	ext := `\.db$`
	lim := WalkLimits{Names: 10, Paths: 10, Words: 60, WordsPerDir: 20, Vectors: 10, VectorsPerDir: 3, StudioText: true, External: &ext}
	vecs := []QueryVector{{Pattern: "%all-MiniLM-L6-v2", Hex: floatsHex(384)}}
	stages := OnePassStages("zanzibar", lim, []string{root}, vecs, nil)
	runner := NewRunner(local, nil, RunnerOptions{Concurrency: 2, Threads: 2})
	out, err := runner.Run(ctx, Command{Tool: ToolQuery, Args: append(append([]string{"-n", "2", "-d", Delim, "-a", "1", "--no-print-sql-on-err"}, stages...), root)})
	if err != nil {
		t.Fatal(err)
	}
	var text, meaning []WalkRow
	for _, r := range WalkRows(string(out.Stdout), []string{root}, []string{root}) {
		switch r.Kind {
		case KindFTS:
			text = append(text, r)
		case KindVector:
			meaning = append(meaning, r)
		}
	}
	if len(text) != 1 || text[0].Name != file[2] || !strings.Contains(text[0].Snippet, "[zanzibar]") {
		t.Errorf("by text want %s alone with its snippet, got %+v", file[2], text)
	}
	if len(meaning) != 1 || meaning[0].Name != file[2] || meaning[0].R > 1e-6 {
		t.Errorf("by meaning want %s alone at distance 0, got %+v", file[2], meaning)
	}
}
