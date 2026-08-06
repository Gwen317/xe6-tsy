package usage

import (
	"crypto/sha256"
	"encoding/json"
)

type recordPayloadHash [sha256.Size]byte

// hashRecordInput 计算完整 RecordInput 的稳定身份摘要。
// 内存和 PostgreSQL Repository 都用它判断：同一幂等键是完全一致的安全重放，还是内容变化的冲突请求。
func hashRecordInput(input RecordInput) (recordPayloadHash, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return recordPayloadHash{}, err
	}
	return recordPayloadHash(sha256.Sum256(payload)), nil
}
