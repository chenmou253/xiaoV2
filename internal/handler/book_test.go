package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"xiaov2/internal/service"

	"github.com/gin-gonic/gin"
)

type emptyBookService struct{}

func (emptyBookService) List(context.Context) ([]service.Book, error) { return []service.Book{}, nil }
func (emptyBookService) Get(context.Context, string) (service.Book, error) {
	return service.Book{}, service.ErrNotFound
}
func (emptyBookService) Pages(context.Context, string) ([]service.PageSummary, error) {
	return nil, service.ErrNotFound
}
func (emptyBookService) Page(context.Context, string, int) (service.PageContent, error) {
	return service.PageContent{}, service.ErrNotFound
}
func (emptyBookService) CoverFile(context.Context, string) (string, error) {
	return "", service.ErrNotFound
}
func (emptyBookService) PageImageFile(context.Context, string, int) (string, error) {
	return "", service.ErrNotFound
}
func (emptyBookService) AudioFile(context.Context, string, int, string, string) (string, error) {
	return "", service.ErrNotFound
}

func TestListBooksReturnsUniformEmptyResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v1/books", NewBookHandler(emptyBookService{}).List)
	request := httptest.NewRequest("GET", "/api/v1/books", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("status = %d", response.Code)
	}
	var body struct {
		Code int            `json:"code"`
		Data []service.Book `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != 0 || body.Data == nil || len(body.Data) != 0 {
		t.Fatalf("unexpected response %s", response.Body.String())
	}
}

func TestInvalidBookIDResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v1/books/:bookId", NewBookHandler(invalidBookService{}).Get)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/books/not-valid", nil))
	if response.Code != 400 {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
}

type invalidBookService struct{ emptyBookService }

func (invalidBookService) Get(context.Context, string) (service.Book, error) {
	return service.Book{}, service.ErrInvalidBookID
}
