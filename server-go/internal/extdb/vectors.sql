-- ext_vectors and its view, as GUFI main's queries read them, run by
-- gufi_sqlite3 (which carries sqlite-vec). Every vector is stored at unit
-- length in the first vector_native_dimensions of its 4096 floats, the
-- rest zero, so cosine distance on the native part, L2 on the whole and
-- a KNN MATCH on the column (declared cosine) all rank alike.

CREATE VIRTUAL TABLE IF NOT EXISTS ext_vectors USING vec0(
    inode INTEGER,
    mtime INTEGER,
    +extra_info TEXT,
    +outer_file_uuid TEXT,
    +asset_id TEXT,
    +embedding_id INTEGER,
    +chunk_id INTEGER,
    vector_mechanism TEXT,
    vector_model_name TEXT,
    vector_native_dimensions INTEGER,
    vector_stored_dimensions INTEGER,
    +page_number INTEGER,
    +audio_start_sec FLOAT,
    +video_start_sec FLOAT,
    +video_end_sec FLOAT,
    +chunk_content TEXT,
    +chunk_prefix TEXT,
    +chunk_overlap TEXT,
    embedding FLOAT32[4096] distance_metric=cosine
);

CREATE VIEW IF NOT EXISTS ext_vectors_v AS
SELECT s.inode,s.mtime,s.extra_info,s.outer_file_uuid,s.asset_id,s.embedding_id,s.chunk_id,s.vector_mechanism,s.vector_model_name,s.vector_native_dimensions,s.vector_stored_dimensions,s.page_number,s.audio_start_sec,s.video_start_sec,s.video_end_sec,s.chunk_content,s.chunk_prefix,s.chunk_overlap,s.embedding,a.component_type,a.member_path_json,a.file_metadata,a.indexing_provenance,a.mimetype,a.asset_route
FROM ext_vectors s
JOIN ext_assets a ON s.asset_id=a.asset_id AND s.outer_file_uuid=a.outer_file_uuid;
