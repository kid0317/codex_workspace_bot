package feishu

import (
	"testing"

	larkdocx "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
)

func TestSanitizeConvertedBlocksRemovesReadOnlyTableMergeInfo(t *testing.T) {
	merge := &larkdocx.TableMergeInfo{}
	blocks := []*larkdocx.Block{{Table: &larkdocx.Table{Property: &larkdocx.TableProperty{MergeInfo: []*larkdocx.TableMergeInfo{merge}}}}}
	sanitizeConvertedBlocks(blocks)
	if blocks[0].Table.Property.MergeInfo != nil {
		t.Fatalf("merge_info was retained: %#v", blocks[0].Table.Property.MergeInfo)
	}
}
