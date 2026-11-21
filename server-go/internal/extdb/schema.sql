-- The "studio" prefix family: the tables of GUFI's common external
-- database schema the Studio writes, as GUFI's examples publish it
-- (examples/ccnext.tar), with the same views. A file is joined back to
-- its entry in db.db by inode and mtime (both int64), and extra_info
-- makes that join unique where a source needs more than the two; the
-- Studio writes one constant string there.
--
-- Text only for now: ext_vectors is a vec0 table, which needs the
-- sqlite-vec extension to create, so a writer without it leaves it out.

CREATE TABLE IF NOT EXISTS ext_assets (inode INTEGER NOT NULL,mtime INTEGER NOT NULL,extra_info TEXT,outer_file_uuid TEXT NOT NULL,asset_id TEXT PRIMARY KEY,component_type TEXT NOT NULL,member_path_json TEXT NOT NULL,file_metadata TEXT,indexing_provenance TEXT,mimetype TEXT,asset_route TEXT);

CREATE VIRTUAL TABLE IF NOT EXISTS ext_bm25 USING fts5(inode UNINDEXED,mtime UNINDEXED,extra_info UNINDEXED,outer_file_uuid UNINDEXED,asset_id UNINDEXED,chunk_sequence UNINDEXED,start_position UNINDEXED,end_position UNINDEXED,page_start UNINDEXED,page_end UNINDEXED,audio_start_sec UNINDEXED,video_start_sec UNINDEXED,video_end_sec UNINDEXED,content);

CREATE VIEW IF NOT EXISTS ext_bm25_v as select e.inode,e.mtime,e.extra_info,e.outer_file_uuid,e.asset_id,e.chunk_sequence,e.start_position,e.end_position,e.page_start,e.page_end,e.audio_start_sec,e.video_start_sec,e.video_end_sec,e.content,a.component_type,a.member_path_json,a.file_metadata,a.indexing_provenance,a.mimetype,a.asset_route,bm25(ext_bm25) as rank FROM ext_bm25 e JOIN ext_assets a ON e.asset_id=a.asset_id AND cast(e.inode as INT64)=a.inode AND cast(e.mtime as INT64)=a.mtime AND e.extra_info=a.extra_info;

CREATE VIEW IF NOT EXISTS ext_assets_by_file_v as select inode,mtime,extra_info,outer_file_uuid,count(*) as asset_count,json_group_array(asset_id) as asset_ids,json_group_array(component_type) as component_types,json_group_array(json(member_path_json)) as member_path_jsons,json_group_array(json(file_metadata)) as file_metadata_jsons,json_group_array(json(member_path_json)) as member_path_jsons,json_group_array(json(indexing_provenance)) as indexing_provenance_jsons,json_group_array(mimetype) as mimetypes,json_group_array(asset_route) as asset_routes from ext_assets group by inode,mtime,extra_info;

CREATE VIEW IF NOT EXISTS ext_assets_by_file_flat_v as SELECT inode,mtime,extra_info,outer_file_uuid,asset_count,(select group_concat(j.value,'|') as asset_ids FROM json_each(v.asset_ids) AS j) as assets_ids,(select group_concat(j.value,'|') as asset_ids FROM json_each(v.component_types) AS j) as component_types,(select group_concat((select group_concat(je.key||'='||je.value) from json_each(j.value) as je),'|') as member_path_jsons FROM json_each(v.member_path_jsons) AS j) member_path_jsons,(select group_concat((select group_concat(je.key||'='||je.value) from json_each(j.value) as je),'|') as file_metadatas_jsons FROM json_each(v.file_metadata_jsons) AS j) as file_metadata_jsons,(select group_concat((select group_concat(je.key||'='||je.value) from json_each(j.value) as je),'|') as indexing_provinance_jsons FROM json_each(v.indexing_provenance_jsons) AS j) as indexing_provenance_jsons,(select group_concat(j.value,'|') FROM json_each(v.mimetypes) AS j) as mimetypes,(select group_concat(j.value,'|') FROM json_each(v.asset_routes) AS j) as asset_routes from ext_assets_by_file_v v group by inode,mtime,extra_info;
