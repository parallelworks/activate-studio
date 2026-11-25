package extdb

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFsid(t *testing.T) {
	t.Setenv(FsidEnv, "site-fs-7")
	if got := Fsid(t.TempDir()); got != "site-fs-7" {
		t.Errorf("the site's fsid: %s", got)
	}
	t.Setenv(FsidEnv, "")
	host, _ := os.Hostname()
	got := Fsid(t.TempDir())
	if !strings.HasPrefix(got, host+":/") {
		t.Errorf("host and mount point: %s", got)
	}
	if _, err := os.Stat("/proc/self/mountinfo"); err == nil {
		if mp := MountPoint("/proc/self"); mp != "/proc" {
			t.Errorf("the mount point of /proc/self: %s", mp)
		}
	}
	if unescapeMount(`/mnt/my\040disk`) != "/mnt/my disk" {
		t.Error(unescapeMount(`/mnt/my\040disk`))
	}
}

// An earlier Studio's text (spread tree under studio/, records with no
// extra_info) is moved and re-keyed, and its shards come out as before.
func TestMigrateAndRekey(t *testing.T) {
	top := filepath.Join(t.TempDir(), "spread")
	old := Spread{Top: top, Prefix: LegacyPrefix, Fsid: "store1"}
	art := artifactWith(t, map[Key]string{{Inode: 101, Mtime: 10}: "alpha text", {Inode: 102, Mtime: 20}: "beta text"})
	if _, err := LoadSpread(old, art); err != nil {
		t.Fatal(err)
	}
	moved, err := MigrateSpread(top)
	if err != nil || !moved {
		t.Fatalf("moved %v %v", moved, err)
	}
	if again, _ := MigrateSpread(top); again {
		t.Error("moved twice")
	}
	if n, err := Rekey(art, "", "store1"); err != nil || n != 2 {
		t.Fatalf("artifact: %d %v", n, err)
	}
	s := Spread{Top: top, Prefix: Prefix, Fsid: "store1"}
	if n, err := RekeySpread(s, "", "store1"); err != nil || n != 2 {
		t.Fatalf("spread: %d %v", n, err)
	}
	a, err := Create(art)
	if err != nil {
		t.Fatal(err)
	}
	states, _ := a.States()
	a.Close()
	if states[Key{Inode: 101, Mtime: 10, ExtraInfo: "store1"}] != StateText || len(states) != 2 {
		t.Errorf("states after: %v", states)
	}
	db, _ := sql.Open("sqlite", s.Path(101))
	var uuid, asset string
	db.QueryRow("SELECT b.outer_file_uuid, b.asset_id FROM ext_bm25 b JOIN ext_assets a ON a.asset_id = b.asset_id AND a.outer_file_uuid = b.outer_file_uuid").Scan(&uuid, &asset)
	db.Close()
	if want := (Key{Inode: 101, Mtime: 10, ExtraInfo: "store1"}).FileUUID(); uuid != want || asset != want {
		t.Errorf("chunk keyed %s %s, want %s", uuid, asset, want)
	}
	ix := t.TempDir()
	uid, gid := int64(os.Getuid()), int64(os.Getgid())
	fakeDir(t, ix, uid, gid, [][5]int64{{101, 10, uid, gid, 0o600}, {102, 20, uid, gid, 0o600}})
	os.WriteFile(filepath.Join(ix, "studio_UR_1_1.db"), []byte("legacy"), 0o600)
	st, err := FillShards(s, ix, "store1")
	if err != nil || st.Files != 2 || st.Legacy != 1 {
		t.Fatalf("%+v %v", st, err)
	}
	if got := texts(t, filepath.Join(ix, "ext_ur.db")); got[101] != "alpha text" || got[102] != "beta text" {
		t.Errorf("shard: %v", got)
	}
}

// gufiSqlite3 is a gufi_sqlite3 with sqlite-vec and sqlite-lembed, and
// model a GGUF model, for the vector tests; they skip without them.
func gufiSqlite3(t *testing.T) (bin, model string) {
	t.Helper()
	dir := os.Getenv("STUDIO_GUFI_BIN")
	if dir == "" {
		dir = "/opt/gufi/bin"
	}
	bin = filepath.Join(dir, "gufi_sqlite3")
	if out, err := exec.Command(bin, ":memory:", "SELECT vec_version(), lembed_version();").CombinedOutput(); err != nil || strings.Contains(string(out), "Error") {
		t.Skipf("no gufi_sqlite3 with sqlite-vec and sqlite-lembed in %s", dir)
	}
	model = os.Getenv("STUDIO_TEST_GGUF")
	if model == "" {
		home, _ := os.UserHomeDir()
		model = filepath.Join(home, "gufi", "models", "minilm384.gguf")
	}
	if _, err := os.Stat(model); err != nil {
		t.Skipf("no GGUF model at %s (STUDIO_TEST_GGUF)", model)
	}
	return bin, model
}

// Vectors made in the artifact reach a shard's ext_vectors at unit length,
// padded to 4096, in a column declared cosine, and the shard holds GUFI's
// schema alone.
func TestVectorsToShards(t *testing.T) {
	bin, model := gufiSqlite3(t)
	art := artifactWith(t, map[Key]string{
		{Inode: 101, Mtime: 10, ExtraInfo: "fs1"}: "campaign storage sits between the parallel file system and tape",
		{Inode: 102, Mtime: 20, ExtraInfo: "fs1"}: "a recipe for bread with flour water salt and yeast",
	})
	e := Embedder{GufiSqlite3: bin, Model: model, ModelName: DefaultModelName, Dims: 384}
	st, err := e.EmbedArtifact(context.Background(), art)
	if err != nil || st.Chunks != 2 {
		t.Fatalf("%+v %v", st, err)
	}
	// Again: nothing new to embed.
	if st, err := e.EmbedArtifact(context.Background(), art); err != nil || st.Chunks != 2 {
		t.Fatalf("again: %+v %v", st, err)
	}
	s := Spread{Top: filepath.Join(t.TempDir(), "spread"), Prefix: Prefix, Fsid: "fs1"}
	if ls, err := LoadSpread(s, art); err != nil || ls.Vectors != 2 {
		t.Fatalf("%+v %v", ls, err)
	}
	ix := t.TempDir()
	uid, gid := int64(os.Getuid()), int64(os.Getgid())
	fakeDir(t, ix, uid, gid, [][5]int64{{101, 10, uid, gid, 0o600}, {102, 20, uid, gid, 0o600}})
	fs, err := FillShardsWith(s, ix, "fs1", ShardOptions{GufiSqlite3: bin})
	if err != nil || fs.Vectors != 1 || fs.VectorError != "" {
		t.Fatalf("%+v %v", fs, err)
	}
	shard := filepath.Join(ix, "ext_ur.db")
	q := func(sql string) string {
		out, err := exec.Command(bin, shard, sql).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v %s", sql, err, out)
		}
		// gufi_sqlite3 separates columns with GUFI's delimiter.
		return strings.ReplaceAll(strings.TrimSpace(string(out)), "\x1e", "|")
	}
	if got := q("SELECT count(*) FROM sqlite_master WHERE name = 'studio_vectors';"); got != "0" {
		t.Errorf("studio_vectors left in the shard: %s", got)
	}
	if got := q("SELECT sql LIKE '%distance_metric=cosine%' FROM sqlite_master WHERE name = 'ext_vectors';"); got != "1" {
		t.Errorf("ext_vectors not declared cosine: %s", got)
	}
	got := q("SELECT count(*), min(vec_length(embedding)), max(vector_native_dimensions), round(min(vec_distance_l2(substr(embedding, 1, 1536), vec_f32(zeroblob(1536)))), 3) FROM ext_vectors_v;")
	if got != "2|4096|384|1.0" {
		t.Errorf("vectors: %s (count, stored, native, unit length)", got)
	}
	// The nearer text to a query about storage is the storage chunk.
	near := q("INSERT INTO temp.lembed_models (name, model) SELECT 'm', lembed_model_from_file('" + model + "'); " +
		"SELECT inode FROM ext_vectors_v ORDER BY vec_distance_cosine(substr(embedding, 1, 1536), lembed('m', 'archival storage tiers')) LIMIT 1;")
	if near != "101" {
		t.Errorf("nearest: %s", near)
	}
}
