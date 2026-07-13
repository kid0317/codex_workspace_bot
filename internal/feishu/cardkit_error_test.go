package feishu

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestCardKitFailureFieldsPreservesOperationAndCode(t *testing.T) {
	fields := cardKitFailureFields(errors.New("network"))
	if fields.operation != "transport" || fields.code != 0 {
		t.Fatalf("transport fields = %#v", fields)
	}
	fields = cardKitFailureFields(&cardKitRequestError{operation: "content", code: 300317})
	if fields.operation != "content" || fields.code != 300317 {
		t.Fatalf("cardkit fields = %#v", fields)
	}
}

func TestCardKitFullUpdatePayloadUsesOneCardJSONEntity(t *testing.T) {
	card, err := cardKitUpdateCard("answer", "progress", true)
	if err != nil {
		t.Fatal(err)
	}
	if card.Type == nil || *card.Type != "card_json" || card.Data == nil {
		t.Fatalf("card = %#v", card)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(*card.Data), &data); err != nil {
		t.Fatal(err)
	}
	if data["schema"] != "2.0" || data["header"] != nil {
		t.Fatalf("full update data = %#v", data)
	}
}

func TestCardKitFailureDoesNotDisableFutureFullEntityUpdates(t *testing.T) {
	sender := &Sender{cards: map[string]*cardSession{"om-card": {cardID: "card-1"}}}
	if !sender.cardKitAvailable() {
		t.Fatal("CardKit should start enabled")
	}
	sender.disableCardKit()
	if !sender.cardKitAvailable() {
		t.Fatal("a single CardKit failure must not force all future cards to PATCH")
	}
	sender.dropCardSession("om-card")
	if sender.cards["om-card"] == nil {
		t.Fatal("a failed full update must retain the CardKit session for the next update")
	}
}
