package feishu

import (
	"fmt"
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

func TestPlanDocumentBlockDescendantBatchesSplitsLargeRootList(t *testing.T) {
	const count = maxDocumentDescendantBlocks + 1
	rootIDs := make([]string, 0, count)
	blocks := make([]*larkdocx.Block, 0, count)
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("block_%04d", i)
		rootIDs = append(rootIDs, id)
		blocks = append(blocks, &larkdocx.Block{BlockId: &id})
	}

	batches, err := planDocumentBlockDescendantBatches(rootIDs, blocks, maxDocumentDescendantBlocks)
	if err != nil {
		t.Fatalf("planDocumentBlockDescendantBatches error = %v", err)
	}
	if len(batches) != 2 {
		t.Fatalf("batch count = %d, want 2", len(batches))
	}
	if got := len(batches[0].Descendants); got != maxDocumentDescendantBlocks {
		t.Fatalf("first batch descendants = %d, want %d", got, maxDocumentDescendantBlocks)
	}
	if got := len(batches[1].Descendants); got != 1 {
		t.Fatalf("second batch descendants = %d, want 1", got)
	}
	if batches[0].ChildrenID[0] != "block_0000" || batches[1].ChildrenID[0] != "block_1000" {
		t.Fatalf("batch roots = %#v / %#v", batches[0].ChildrenID, batches[1].ChildrenID)
	}
}

func TestPlanDocumentBlockDescendantBatchesKeepsNestedSubtreesTogether(t *testing.T) {
	root1, child1, root2 := "root_1", "child_1", "root_2"
	blocks := []*larkdocx.Block{
		{BlockId: &root1, Children: []string{child1}},
		{BlockId: &child1},
		{BlockId: &root2},
	}

	batches, err := planDocumentBlockDescendantBatches([]string{root1, root2}, blocks, 2)
	if err != nil {
		t.Fatalf("planDocumentBlockDescendantBatches error = %v", err)
	}
	if len(batches) != 2 || len(batches[0].Descendants) != 2 || len(batches[1].Descendants) != 1 {
		t.Fatalf("batches = %#v", batches)
	}
	if got := batches[0].ChildrenID; len(got) != 1 || got[0] != root1 {
		t.Fatalf("first batch roots = %#v", got)
	}

	_, err = planDocumentBlockDescendantBatches([]string{root1}, blocks[:2], 1)
	if err == nil {
		t.Fatalf("oversized subtree error is nil")
	}
}
