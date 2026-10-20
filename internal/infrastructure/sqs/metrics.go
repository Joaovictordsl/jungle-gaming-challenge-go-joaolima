package sqs

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	sdk "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

func (c *Client) DLQMetrics(ctx context.Context) (string, error) {
	return c.dlqMetrics(ctx, "commands")
}
func (c *PublisherClient) DLQMetrics(ctx context.Context) (string, error) {
	if c == nil {
		return "", errors.New("publisher não iniciado")
	}
	return c.client.dlqMetrics(ctx, "events")
}
func (c *Client) dlqMetrics(ctx context.Context, queue string) (string, error) {
	if c == nil || c.API == nil || c.DLQURL == "" {
		return "", errors.New("DLQ não iniciada")
	}
	attributes := []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible, types.QueueAttributeNameApproximateNumberOfMessagesDelayed}
	result, err := c.API.GetQueueAttributes(ctx, &sdk.GetQueueAttributesInput{QueueUrl: aws.String(c.DLQURL), AttributeNames: attributes})
	if err != nil {
		return "", err
	}
	var out strings.Builder
	// TYPE é emitido apenas uma vez quando ambas as fontes estão habilitadas.
	for i, state := range []string{"visible", "in_flight", "delayed"} {
		count, err := strconv.ParseUint(result.Attributes[string(attributes[i])], 10, 64)
		if err != nil {
			return "", errors.New("atributos numéricos de DLQ inválidos")
		}
		fmt.Fprintf(&out, "jungle_sqs_dlq_messages{queue=%q,state=%q} %d\n", queue, state, count)
	}
	return out.String(), nil
}
