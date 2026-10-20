package message

import (
	"encoding/json"
	"strings"
	"testing"
)

const validBody = `{"messageId":"msg-1","type":"WagerTransactionRequested","occurredAt":"2026-10-19T10:00:00Z","data":{"providerId":"provider-a","externalTransactionId":"bet-1","idempotencyKey":"key","walletId":"wallet","playerId":"player","roundId":"round","gameId":"game","kind":"BET","money":{"amount":"80.00","currency":"BRL"}}}`

func TestEnvelopeNormalizationAndFinancialIdentity(t *testing.T) {
	first, err := Parse([]byte(validBody))
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := first.Hash()
	financial, _ := first.Command.PayloadHash()
	equivalent, err := Parse([]byte(strings.Replace(strings.Replace(validBody, `"80.00"`, `"00080.00"`, 1), `10:00:00Z`, `07:00:00-03:00`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	same, _ := equivalent.Hash()
	if same != hash {
		t.Fatal("Normalização alterou hash da mensagem")
	}
	changed := first
	changed.MessageID = "msg-2"
	changed.OccurredAt = changed.OccurredAt.Add(1)
	different, _ := changed.Hash()
	sameFinancial, _ := changed.Command.PayloadHash()
	if hash == different || financial != sameFinancial {
		t.Fatal("Metadados misturados com identidade financeira")
	}
	changed = first
	changed.Command.IdempotencyKey = "other"
	different, _ = changed.Hash()
	sameFinancial, _ = changed.Command.PayloadHash()
	if different == hash || financial != sameFinancial {
		t.Fatal("Chave não foi separada do hash financeiro")
	}
	data, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := Parse(data)
	if err != nil || restored != first {
		t.Fatal("Envelope não preservado", err)
	}
	if GroupID("wallet") == GroupID("other") || len(GroupID(strings.Repeat("w", 256))) > 128 {
		t.Fatal("Grupo de carteira inválido")
	}
}
func TestEnvelopeRejectsInvalidInputs(t *testing.T) {
	for _, body := range []string{
		strings.Replace(validBody, `"messageId":"msg-1"`, `"messageId":"msg-1","messageId":"msg-2"`, 1),
		strings.Replace(validBody, `"messageId":"msg-1"`, `"messageId":null`, 1),
		strings.Replace(validBody, `"type":"WagerTransactionRequested"`, `"type":"Other"`, 1),
		strings.Replace(validBody, `"kind":"BET"`, `"kind":"OPENING"`, 1),
		strings.Replace(validBody, `"kind":"BET"`, `"kind":"ROLLBACK"`, 1),
		strings.Replace(validBody, `"amount":"80.00"`, `"amount":80.00`, 1),
		strings.Replace(validBody, `"amount":"80.00"`, `"amount":"80.00","amount":"1.00"`, 1),
		strings.Replace(validBody, `"occurredAt":"2026-10-19T10:00:00Z"`, `"occurredAt":"yesterday"`, 1),
		strings.Replace(validBody, `"idempotencyKey":"key",`, "", 1),
		strings.Replace(validBody, `"idempotencyKey":"key"`, `"idempotencyKey":""`, 1),
		strings.Replace(validBody, `"gameId":"game"`, `"gameId":"game","unknown":true`, 1),
		validBody + ` {}`, strings.Repeat(" ", MaxBodyBytes) + validBody,
	} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Fatal("Envelope inválido aceito")
		}
	}
	for _, kind := range []string{"BET", "WIN", "LOSS", "REFUND", "ROLLBACK"} {
		body := strings.Replace(validBody, `"kind":"BET"`, `"kind":"`+kind+`"`, 1)
		if kind == "LOSS" {
			body = strings.Replace(body, `"80.00"`, `"0.00"`, 1)
		}
		if kind == "REFUND" || kind == "ROLLBACK" {
			body = strings.Replace(body, `"kind":"`+kind+`"`, `"kind":"`+kind+`","referenceExternalTransactionId":"original"`, 1)
		}
		if _, err := Parse([]byte(body)); err != nil {
			t.Fatal(kind, err)
		}
	}
}
