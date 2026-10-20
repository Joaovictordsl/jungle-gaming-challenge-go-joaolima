package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	sdk "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.uber.org/fx"
	"jungle-gaming-challenge-go-joaolima/internal/config"
)

const MaxReceives = 5

type Delivery struct {
	BrokerID, Receipt, Body, GroupID string
	Attempt                          int
}
type Broker interface {
	Receive(context.Context) (*Delivery, error)
	Delete(context.Context, string) error
	Release(context.Context, string, int32) error
}
type Client struct {
	API              *sdk.Client
	QueueURL, DLQURL string
	transport        *http.Transport
}

func NewClient(lc fx.Lifecycle, c config.SQS) (*Client, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	client := new(Client)
	if !c.Enabled {
		return client, nil
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = c.Workers + 2
	client.transport = transport
	client.API = sdk.NewFromConfig(aws.Config{Region: c.Region, Credentials: credentials.NewStaticCredentialsProvider(c.AccessKeyID, c.SecretAccessKey, ""), HTTPClient: &http.Client{Transport: transport, Timeout: 25 * time.Second}}, func(o *sdk.Options) { o.BaseEndpoint = aws.String(c.Endpoint) })
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		err := client.Check(ctx, c)
		if err != nil {
			transport.CloseIdleConnections()
		}
		return err
	}, OnStop: func(context.Context) error { transport.CloseIdleConnections(); return nil }})
	return client, nil
}
func (c *Client) Check(ctx context.Context, cfg config.SQS) error {
	source, err := c.API.GetQueueUrl(ctx, &sdk.GetQueueUrlInput{QueueName: aws.String(cfg.QueueName)})
	if err != nil {
		return errors.New("fila SQS indisponível ou acesso negado")
	}
	dlq, err := c.API.GetQueueUrl(ctx, &sdk.GetQueueUrlInput{QueueName: aws.String(cfg.DLQName)})
	if err != nil {
		return errors.New("DLQ indisponível ou acesso negado")
	}
	sourceURL, dlqURL := aws.ToString(source.QueueUrl), aws.ToString(dlq.QueueUrl)
	if err := c.checkPair(ctx, sourceURL, dlqURL); err != nil {
		return err
	}
	c.QueueURL, c.DLQURL = sourceURL, dlqURL
	return nil
}

// Ready consulta atributos sem mudar URLs compartilhadas com os pollers.
func (c *Client) Ready(ctx context.Context) error {
	if c == nil || c.API == nil || c.QueueURL == "" || c.DLQURL == "" {
		return errors.New("cliente SQS não iniciado")
	}
	return c.checkPair(ctx, c.QueueURL, c.DLQURL)
}
func (c *Client) checkPair(ctx context.Context, sourceURL, dlqURL string) error {
	a, err := c.API.GetQueueAttributes(ctx, &sdk.GetQueueAttributesInput{QueueUrl: aws.String(sourceURL), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameAll}})
	if err != nil {
		return errors.New("atributos SQS indisponíveis")
	}
	b, err := c.API.GetQueueAttributes(ctx, &sdk.GetQueueAttributesInput{QueueUrl: aws.String(dlqURL), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameAll}})
	if err != nil {
		return errors.New("atributos DLQ indisponíveis")
	}
	var redrive struct {
		ARN   string          `json:"deadLetterTargetArn"`
		Count json.RawMessage `json:"maxReceiveCount"`
	}
	if json.Unmarshal([]byte(a.Attributes["RedrivePolicy"]), &redrive) != nil || redrive.ARN != b.Attributes["QueueArn"] || (string(redrive.Count) != strconv.Itoa(MaxReceives) && string(redrive.Count) != `"`+strconv.Itoa(MaxReceives)+`"`) || a.Attributes["FifoQueue"] != "true" || b.Attributes["FifoQueue"] != "true" || a.Attributes["VisibilityTimeout"] != "30" {
		return errors.New("configuração FIFO/redrive inesperada")
	}
	return nil
}
func (c *Client) Receive(ctx context.Context) (*Delivery, error) {
	out, err := c.API.ReceiveMessage(ctx, &sdk.ReceiveMessageInput{QueueUrl: aws.String(c.QueueURL), MaxNumberOfMessages: 1, WaitTimeSeconds: 20, VisibilityTimeout: 30, MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll}})
	if err != nil {
		return nil, err
	}
	if len(out.Messages) == 0 {
		return nil, nil
	}
	m := out.Messages[0]
	attempt, err := strconv.Atoi(m.Attributes["ApproximateReceiveCount"])
	if err != nil || attempt < 1 {
		attempt = MaxReceives
	}
	return &Delivery{BrokerID: aws.ToString(m.MessageId), Receipt: aws.ToString(m.ReceiptHandle), Body: aws.ToString(m.Body), GroupID: m.Attributes["MessageGroupId"], Attempt: attempt}, nil
}
func (c *Client) Delete(ctx context.Context, receipt string) error {
	_, err := c.API.DeleteMessage(ctx, &sdk.DeleteMessageInput{QueueUrl: aws.String(c.QueueURL), ReceiptHandle: aws.String(receipt)})
	return err
}
func (c *Client) Release(ctx context.Context, receipt string, delay int32) error {
	_, err := c.API.ChangeMessageVisibility(ctx, &sdk.ChangeMessageVisibilityInput{QueueUrl: aws.String(c.QueueURL), ReceiptHandle: aws.String(receipt), VisibilityTimeout: delay})
	return err
}
