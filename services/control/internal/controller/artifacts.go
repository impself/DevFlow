package controller

// artifacts.go（T018）：分析产物的持久化——正文落磁盘（content-addressable），
// 表里只存引用与哈希（PRD §18.1）。reply_drafts 维护「每 case 一个 current 草稿」。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/impself/DevFlow/services/control/internal/contract"
	"github.com/impself/DevFlow/services/control/internal/ids"
	"github.com/impself/DevFlow/services/control/internal/store"
	"github.com/impself/DevFlow/services/control/internal/store/db"
)

// ArtifactStore 落产物文件 + 登记引用。
// 文件路径 = <dir>/<digest 前 2 位>/<digest>：前缀分片避免单目录文件数爆炸
// （git objects 同款布局）；同 digest = 同字节，重复写入安全跳过。
type ArtifactStore struct {
	st  *store.Store
	dir string
}

func NewArtifactStore(st *store.Store, dir string) *ArtifactStore {
	return &ArtifactStore{st: st, dir: dir}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// write 把内容写到磁盘并登记 artifacts 表，返回 (artifactID, digest)。
// 文件先写、行后插：DB 失败只留下孤儿文件（content-addressable 下无害且天然去重）；
// 反过来先插行后写文件会留下「库里有引用、盘上无字节」的坏状态。
func (a *ArtifactStore) write(ctx context.Context, runID, kind string, content []byte) (string, string, error) {
	digest := sha256Hex(content)
	path := filepath.Join(a.dir, digest[:2], digest)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", "", fmt.Errorf("创建产物目录: %w", err)
		}
		// 0o644：产物非敏感（草稿与证据），但保持只读纪律
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return "", "", fmt.Errorf("写产物文件: %w", err)
		}
	}

	artifactID := ids.New("art")
	_, err := a.st.InsertArtifact(ctx, db.InsertArtifactParams{
		ID:            artifactID,
		RunID:         runID,
		Kind:          kind,
		ContentDigest: digest,
		SizeBytes:     int32(len(content)),
		SchemaVersion: "1",
	})
	if errors.Is(err, store.ErrNoRows) {
		// 同 digest 已登记：复用现有行（content-addressable 幂等）
		existing, e := a.st.GetArtifactByDigest(ctx, digest)
		if e != nil {
			return "", "", e
		}
		return existing.ID, digest, nil
	}
	if err != nil {
		return "", "", err
	}
	return artifactID, digest, nil
}

// SaveDraft 保存一次分析的产物：草稿正文 / 证据包 / 原始模型输出三个 artifact
// + 一行 reply_drafts（同 case 旧草稿翻 superseded）。
//
// body_digest 是草稿内容的 SHA-256——审批绑定的内容锚（AC33）：
// 批准时比对它，内容变过就失效。
func (a *ArtifactStore) SaveDraft(ctx context.Context, runID, caseID string, out *contract.AgentOutput, raw []byte) error {
	// 草稿正文：ANSWER_READY 是回复正文；NEEDS_INFO 把追问渲染成正文
	// （approval 永远不绑 NEEDS_INFO，digest 语义无副作用）
	body := []byte(out.ReplyMarkdown)
	if out.Outcome == "NEEDS_INFO" {
		body, _ = json.Marshal(out.NeedsInfoQuestions)
	}
	bodyID, bodyDigest, err := a.write(ctx, runID, "reply_draft", body)
	if err != nil {
		return fmt.Errorf("写草稿正文: %w", err)
	}

	evidenceJSON, err := json.Marshal(out.Evidence)
	if err != nil {
		return err
	}
	if _, _, err := a.write(ctx, runID, "evidence_pack", evidenceJSON); err != nil {
		return fmt.Errorf("写证据包: %w", err)
	}
	if _, _, err := a.write(ctx, runID, "raw_model_io", raw); err != nil {
		return fmt.Errorf("写原始模型输出: %w", err)
	}

	var questions []byte
	if len(out.NeedsInfoQuestions) > 0 {
		questions, err = json.Marshal(out.NeedsInfoQuestions)
		if err != nil {
			return err
		}
	}

	// supersede + insert 同一事务：「每 case 恰一个 current」不变量
	return a.st.WithTx(ctx, func(q *db.Queries) error {
		if _, err := q.SupersedeCaseDrafts(ctx, caseID); err != nil {
			return err
		}
		return q.InsertReplyDraft(ctx, db.InsertReplyDraftParams{
			ID:                 ids.New("draft"),
			RunID:              runID,
			ArtifactID:         bodyID,
			Conclusion:         out.Outcome,
			BodyDigest:         bodyDigest,
			Evidence:           evidenceJSON,
			NeedsInfoQuestions: questions,
		})
	})
}
