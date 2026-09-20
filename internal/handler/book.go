package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"

	"xiaov2/internal/service"

	"github.com/gin-gonic/gin"
)

type BookUseCase interface {
	List(context.Context) ([]service.Book, error)
	Get(context.Context, string) (service.Book, error)
	Pages(context.Context, string) ([]service.PageSummary, error)
	Page(context.Context, string, int) (service.PageContent, error)
	CoverFile(context.Context, string) (string, error)
	PageImageFile(context.Context, string, int) (string, error)
	AudioFile(context.Context, string, int, string, string) (string, error)
}

type BookHandler struct{ service BookUseCase }

func NewBookHandler(bookService BookUseCase) *BookHandler { return &BookHandler{service: bookService} }

func (h *BookHandler) List(c *gin.Context) {
	books, err := h.service.List(c.Request.Context())
	if err != nil {
		h.internal(c, err)
		return
	}
	success(c, books)
}

func (h *BookHandler) Get(c *gin.Context) {
	book, err := h.service.Get(c.Request.Context(), c.Param("bookId"))
	if err != nil {
		h.writeError(c, err)
		return
	}
	success(c, book)
}

func (h *BookHandler) Pages(c *gin.Context) {
	pages, err := h.service.Pages(c.Request.Context(), c.Param("bookId"))
	if err != nil {
		h.writeError(c, err)
		return
	}
	success(c, pages)
}

func (h *BookHandler) Page(c *gin.Context) {
	page, ok := h.pageNumber(c)
	if !ok {
		return
	}
	content, err := h.service.Page(c.Request.Context(), c.Param("bookId"), page)
	if err != nil {
		h.writeError(c, err)
		return
	}
	success(c, content)
}

func (h *BookHandler) Cover(c *gin.Context) {
	path, err := h.service.CoverFile(c.Request.Context(), c.Param("bookId"))
	h.file(c, path, err)
}

func (h *BookHandler) PageImage(c *gin.Context) {
	page, ok := h.pageNumber(c)
	if !ok {
		return
	}
	path, err := h.service.PageImageFile(c.Request.Context(), c.Param("bookId"), page)
	h.file(c, path, err)
}

func (h *BookHandler) Audio(c *gin.Context) {
	page, ok := h.pageNumber(c)
	if !ok {
		return
	}
	path, err := h.service.AudioFile(c.Request.Context(), c.Param("bookId"), page, c.Param("itemId"), c.Query("accent"))
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("Content-Type", "audio/wav")
	c.File(path)
}

func (h *BookHandler) file(c *gin.Context, path string, err error) {
	if err != nil {
		h.writeError(c, err)
		return
	}
	info, statErr := os.Stat(path)
	if statErr != nil || !info.Mode().IsRegular() {
		h.writeError(c, service.ErrNotFound)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.File(path)
}

func (h *BookHandler) pageNumber(c *gin.Context) (int, bool) {
	page, err := strconv.Atoi(c.Param("page"))
	if err != nil || page < 1 {
		failure(c, http.StatusBadRequest, 40002, "invalid page")
		return 0, false
	}
	return page, true
}

func (h *BookHandler) writeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidBookID):
		failure(c, http.StatusBadRequest, 40001, "invalid book id")
	case errors.Is(err, service.ErrInvalidPage):
		failure(c, http.StatusBadRequest, 40002, "invalid page")
	case errors.Is(err, service.ErrNotFound), errors.Is(err, os.ErrNotExist):
		failure(c, http.StatusNotFound, 40401, "book resource not found")
	default:
		h.internal(c, err)
	}
}

func (h *BookHandler) internal(c *gin.Context, err error) {
	log.Printf("request failed: %v", err)
	failure(c, http.StatusInternalServerError, 50001, "internal server error")
}
