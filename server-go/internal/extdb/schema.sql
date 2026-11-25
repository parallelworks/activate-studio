-- The "ext" prefix family: GUFI's common external database schema, as
-- GUFI main's examples and queries read it, table for table and view for
-- view. A file is joined back to its entry in db.db by inode and mtime;
-- extra_info qualifies the inode with the file system's id (fsid), the
-- same for every file of one file system at a site. Each file has at
-- least one ext_assets row, its own ('file', asset_id = outer_file_uuid),
-- which its ext_bm25 and ext_vectors rows join to by asset_id and
-- outer_file_uuid.
--
-- ext_vectors is a vec0 table, which needs sqlite-vec to create; it and
-- its view are in vectors.sql, run by gufi_sqlite3.

CREATE TABLE IF NOT EXISTS ext_assets (
    inode INTEGER NOT NULL,
    mtime INTEGER NOT NULL,
    extra_info TEXT,
    outer_file_uuid TEXT NOT NULL,
    asset_id TEXT PRIMARY KEY,
    component_type TEXT NOT NULL,
    member_path_json TEXT NOT NULL,
    file_metadata TEXT,
    indexing_provenance TEXT,
    mimetype TEXT,
    asset_route TEXT
);

CREATE VIRTUAL TABLE IF NOT EXISTS ext_bm25 USING fts5(
    inode UNINDEXED,
    mtime UNINDEXED,
    extra_info UNINDEXED,
    outer_file_uuid UNINDEXED,
    asset_id UNINDEXED,
    chunk_sequence UNINDEXED,
    start_position UNINDEXED,
    end_position UNINDEXED,
    page_start UNINDEXED,
    page_end UNINDEXED,
    audio_start_sec UNINDEXED,
    video_start_sec UNINDEXED,
    video_end_sec UNINDEXED,
    content
);

CREATE VIEW IF NOT EXISTS ext_bm25_v AS
SELECT s.inode,s.mtime,s.extra_info,s.outer_file_uuid,s.asset_id,s.chunk_sequence,s.start_position,s.end_position,s.page_start,s.page_end,s.audio_start_sec,s.video_start_sec,s.video_end_sec,s.content,a.component_type,a.member_path_json,a.file_metadata,a.indexing_provenance,a.mimetype,a.asset_route,bm25(ext_bm25) AS rank
FROM ext_bm25 s
JOIN ext_assets a ON s.asset_id=a.asset_id AND s.outer_file_uuid=a.outer_file_uuid;

CREATE VIEW IF NOT EXISTS ext_assets_by_file_v AS
SELECT outer_file_uuid,inode,mtime,extra_info,count(*) AS asset_count,
       json_group_array(json(asset_json)) AS assets_json
FROM (SELECT outer_file_uuid,inode,mtime,extra_info,json_object(
        'asset_id',asset_id,'component_type',component_type,
        'member_path_json',json(member_path_json),'file_metadata',json(file_metadata),
        'indexing_provenance',json(indexing_provenance),'mimetype',mimetype,
        'asset_route',asset_route) AS asset_json
      FROM ext_assets ORDER BY outer_file_uuid,asset_id)
GROUP BY outer_file_uuid,inode,mtime,extra_info;

CREATE VIEW IF NOT EXISTS ext_assets_by_file_sv as select inode,mtime,extra_info,outer_file_uuid,count(*) as asset_count,json_group_array(asset_id) as asset_ids,json_group_array(component_type) as component_types,json_group_array(json(member_path_json)) as member_path_jsons,json_group_array(json(file_metadata)) as file_metadata_jsons,json_group_array(json(member_path_json)) as member_path_jsons,json_group_array(json(indexing_provenance)) as indexing_provenance_jsons,json_group_array(mimetype) as mimetypes,json_group_array(asset_route) as asset_routes from ext_assets group by inode,mtime,extra_info;

CREATE VIEW IF NOT EXISTS ext_assets_by_file_flat_sv as SELECT inode,mtime,extra_info,outer_file_uuid,asset_count,(select group_concat(j.value,'|') as asset_ids FROM json_each(v.asset_ids) AS j) as assets_ids,(select group_concat(j.value,'|') as asset_ids FROM json_each(v.component_types) AS j) as component_types,(select group_concat((select group_concat(je.key||'='||je.value) from json_each(j.value) as je),'|') as member_path_jsons FROM json_each(v.member_path_jsons) AS j) member_path_jsons,(select group_concat((select group_concat(je.key||'='||je.value) from json_each(j.value) as je),'|') as file_metadatas_jsons FROM json_each(v.file_metadata_jsons) AS j) as file_metadata_jsons,(select group_concat((select group_concat(je.key||'='||je.value) from json_each(j.value) as je),'|') as indexing_provinance_jsons FROM json_each(v.indexing_provenance_jsons) AS j) as indexing_provenance_jsons,(select group_concat(j.value,'|') FROM json_each(v.mimetypes) AS j) as mimetypes,(select group_concat(j.value,'|') FROM json_each(v.asset_routes) AS j) as asset_routes from ext_assets_by_file_sv v group by inode,mtime,extra_info;

