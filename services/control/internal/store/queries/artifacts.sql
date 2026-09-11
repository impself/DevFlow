-- artifacts：不可变产物（content-addressable，正文在磁盘、库存引用与哈希）

-- 同 digest = 同内容：ON CONFLICT 复用已有行（文件层天然去重，表层同样去重）。
-- 冲突时无返回行（ErrNoRows），调用方经 GetArtifactByDigest 取现有 id。
-- name: InsertArtifact :one
INSERT INTO artifacts (id, run_id, kind, content_digest, size_bytes, schema_version)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (content_digest) DO NOTHING
RETURNING id;

-- name: GetArtifactByDigest :one
SELECT * FROM artifacts WHERE content_digest = $1;
