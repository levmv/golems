package openai

import (
	"context"
	"errors"
	"net/http"
)

var ErrVectorLengthMismatch = errors.New("vector length mismatch")

// EmbeddingModel enumerates the models which can be used
// to generate Embedding vectors.
type EmbeddingModel string

const (
	AdaEmbeddingV2  EmbeddingModel = "text-embedding-ada-002"
	SmallEmbedding3 EmbeddingModel = "text-embedding-3-small"
	LargeEmbedding3 EmbeddingModel = "text-embedding-3-large"
)

type Embedding struct {
	Object    string    `json:"object"`
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

// DotProduct calculates the dot product of the embedding vector with another
// embedding vector. Both vectors must have the same length; otherwise, an
// ErrVectorLengthMismatch is returned. The method returns the calculated dot
// product as a float32 value.
func (e *Embedding) DotProduct(other *Embedding) (float32, error) {
	if len(e.Embedding) != len(other.Embedding) {
		return 0, ErrVectorLengthMismatch
	}

	var dotProduct float32
	for i := range e.Embedding {
		dotProduct += e.Embedding[i] * other.Embedding[i]
	}

	return dotProduct, nil
}

// EmbeddingResponse is the response from a Create embeddings request.
type EmbeddingResponse struct {
	Object string         `json:"object"`
	Data   []Embedding    `json:"data"`
	Model  EmbeddingModel `json:"model"`
	Usage  Usage          `json:"usage"`
}

// EmbeddingEncodingFormat is the format of the embeddings data.
type EmbeddingEncodingFormat string

const (
	EmbeddingEncodingFormatFloat EmbeddingEncodingFormat = "float"
)

type EmbeddingRequest struct {
	// Input can be a string, []string, []int, or [][]int
	Input          any                     `json:"input"`
	Model          EmbeddingModel          `json:"model"`
	User           string                  `json:"user,omitempty"`
	EncodingFormat EmbeddingEncodingFormat `json:"encoding_format,omitempty"`
	Dimensions     int                     `json:"dimensions,omitempty"`
}

// CreateEmbeddings returns an EmbeddingResponse which will contain an Embedding for every item in Input.
func (c *Client) CreateEmbeddings(
	ctx context.Context,
	request EmbeddingRequest,
) (EmbeddingResponse, error) {
	// If not specified, default to float
	if request.EncodingFormat == "" {
		request.EncodingFormat = EmbeddingEncodingFormatFloat
	}

	req, err := c.newRequest(
		ctx,
		http.MethodPost,
		c.fullURL("/embeddings"),
		request,
	)
	if err != nil {
		return EmbeddingResponse{}, err
	}

	var res EmbeddingResponse
	err = c.sendRequest(req, &res)
	if err != nil {
		return EmbeddingResponse{}, err
	}

	return res, nil
}
