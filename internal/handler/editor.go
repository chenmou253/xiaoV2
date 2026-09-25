package handler

import (
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"xiaov2/internal/ai"
	"xiaov2/internal/service"
)

type EditorHandler struct{ service *service.EditorService }

func NewEditorHandler(s *service.EditorService) *EditorHandler { return &EditorHandler{service: s} }
func (h *EditorHandler) List(c *gin.Context) {
	v, e := h.service.List(c.Request.Context())
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, v)
}
func (h *EditorHandler) Books(c *gin.Context) {
	v, e := h.service.Books(c.Request.Context())
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, v)
}
func (h *EditorHandler) SaveBook(c *gin.Context) {
	var in struct {
		Title     string `json:"title"`
		Grade     string `json:"grade"`
		Semester  string `json:"semester"`
		Publisher string `json:"publisher"`
		Status    string `json:"status"`
		Published *bool  `json:"published"`
		Revision  uint64 `json:"revision"`
	}
	if !bindJSON(c, &in) {
		return
	}
	if in.Published != nil {
		if *in.Published {
			in.Status = "published"
		} else {
			in.Status = "draft"
		}
	}
	if e := h.service.SaveBook(c.Request.Context(), c.Param("bookId"), in.Title, in.Grade, in.Semester, in.Publisher, in.Status, in.Revision, identity(c).ID); e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"ok": true})
}
func (h *EditorHandler) PublishedPages(c *gin.Context) {
	v, e := h.service.PublishedPages(c.Request.Context(), c.Param("bookId"))
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, v)
}
func (h *EditorHandler) PublishedPage(c *gin.Context) {
	p, ok := pageParam(c)
	if !ok {
		return
	}
	v, e := h.service.PublishedPage(c.Request.Context(), c.Param("bookId"), p)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, v)
}
func (h *EditorHandler) Upload(c *gin.Context) {
	if e := c.Request.ParseMultipartForm(2 << 20); e != nil {
		failure(c, 400, 40000, "PDF 上传失败，文件上限 128MB")
		return
	}
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}
	f, _, e := c.Request.FormFile("file")
	if e != nil {
		failure(c, 400, 40000, "请选择 PDF 文件")
		return
	}
	defer f.Close()
	head := make([]byte, 5)
	if _, e = io.ReadFull(f, head); e != nil || string(head) != "%PDF-" {
		failure(c, 400, 40000, "文件不是有效的 PDF")
		return
	}
	tmp, e := os.CreateTemp("", "xiaov2-upload-*.pdf")
	if e != nil {
		writePlatformError(c, e)
		return
	}
	name := tmp.Name()
	defer os.Remove(name)
	defer tmp.Close()
	if _, e = tmp.Write(head); e == nil {
		_, e = io.Copy(tmp, io.LimitReader(f, (128<<20)+1))
	}
	if e != nil {
		writePlatformError(c, e)
		return
	}
	info, _ := tmp.Stat()
	if info.Size() > 128<<20 {
		failure(c, 400, 40000, "PDF 超过 128MB")
		return
	}
	grade, _ := strconv.Atoi(c.PostForm("grade"))
	audio := service.DefaultAudioSettings()
	if raw, exists := c.GetPostForm("american_enabled"); exists {
		audio.AmericanEnabled = formBool(raw)
	}
	if raw, exists := c.GetPostForm("british_enabled"); exists {
		audio.BritishEnabled = formBool(raw)
	}
	if value := c.PostForm("american_voice_id"); value != "" {
		audio.AmericanVoiceID = value
	}
	if value := c.PostForm("british_voice_id"); value != "" {
		audio.BritishVoiceID = value
	}
	id, e := h.service.CreateUpload(c.Request.Context(), c.PostForm("book_id"), c.PostForm("title"), grade, c.PostForm("term"), c.PostForm("edition"), name, identity(c).ID, audio)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"code": 0, "message": "ok", "data": gin.H{"id": id}})
}
func (h *EditorHandler) Copy(c *gin.Context) {
	id, e := h.service.CopyPublished(c.Request.Context(), c.Param("bookId"), identity(c).ID)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"id": id})
}
func (h *EditorHandler) Get(c *gin.Context) {
	v, e := h.service.Get(c.Request.Context(), c.Param("draftId"))
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, v)
}
func (h *EditorHandler) Status(c *gin.Context) {
	v, e := h.service.DraftStatus(c.Request.Context(), c.Param("draftId"))
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, v)
}
func (h *EditorHandler) AudioReviewQueue(c *gin.Context) {
	v, e := h.service.AudioReviewQueue(c.Request.Context())
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, v)
}
func (h *EditorHandler) Delete(c *gin.Context) {
	if e := h.service.Delete(c.Request.Context(), c.Param("draftId"), identity(c).ID); e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"ok": true})
}
func (h *EditorHandler) Page(c *gin.Context) {
	p, ok := pageParam(c)
	if !ok {
		return
	}
	v, e := h.service.Page(c.Request.Context(), c.Param("draftId"), p)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, v)
}
func (h *EditorHandler) Image(c *gin.Context) {
	p, ok := pageParam(c)
	if !ok {
		return
	}
	path, e := h.service.Image(c.Request.Context(), c.Param("draftId"), p)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	contentType := "image/webp"
	if detected := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); detected != "" {
		contentType = detected
	}
	serveFile(c, path, contentType)
}
func (h *EditorHandler) Audio(c *gin.Context) {
	p, ok := pageParam(c)
	if !ok {
		return
	}
	path, e := h.service.Audio(c.Request.Context(), c.Param("draftId"), p, c.Param("itemId"), c.Query("accent"))
	if e != nil {
		writePlatformError(c, e)
		return
	}
	serveFile(c, path, "audio/wav")
}
func (h *EditorHandler) AudioIssues(c *gin.Context) {
	p, ok := pageParam(c)
	if !ok {
		return
	}
	issues, e := h.service.AudioIssues(c.Request.Context(), c.Param("draftId"), p)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, issues)
}
func (h *EditorHandler) TranslationIssues(c *gin.Context) {
	p, ok := pageParam(c)
	if !ok {
		return
	}
	issues, e := h.service.TranslationIssues(c.Request.Context(), c.Param("draftId"), p)
	if e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, issues)
}

func (h *EditorHandler) ResolveTranslationIssue(c *gin.Context) {
	p, ok := pageParam(c)
	if !ok {
		return
	}
	var in struct {
		Revision    uint64 `json:"revision"`
		Translation string `json:"translation"`
		Meaning     string `json:"meaning"`
		Phonetic    string `json:"phonetic"`
	}
	if !bindJSON(c, &in) {
		return
	}
	if e := h.service.ResolveTranslationIssue(
		c.Request.Context(),
		c.Param("draftId"),
		p,
		c.Param("itemId"),
		in.Revision,
		in.Translation,
		in.Meaning,
		in.Phonetic,
		identity(c).ID,
	); e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"ok": true})
}

func (h *EditorHandler) FailedAudio(c *gin.Context) {
	p, ok := pageParam(c)
	if !ok {
		return
	}
	path, e := h.service.FailedAudio(c.Request.Context(), c.Param("draftId"), p, c.Param("itemId"), c.Query("accent"))
	if e != nil {
		writePlatformError(c, e)
		return
	}
	serveFile(c, path, "audio/wav")
}
func (h *EditorHandler) RegenerateAudioItem(c *gin.Context) {
	p, ok := pageParam(c)
	if !ok {
		return
	}
	var in struct {
		Accent  string `json:"accent"`
		Text    string `json:"text"`
		Version uint64 `json:"version"`
	}
	if !bindJSON(c, &in) {
		return
	}
	if e := h.service.RegenerateAudioItem(c.Request.Context(), c.Param("draftId"), p, c.Param("itemId"), in.Accent, in.Text, in.Version, identity(c).ID); e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"ok": true})
}

func (h *EditorHandler) UploadAudioItem(c *gin.Context) {
	p, ok := pageParam(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 12<<20)
	if err := c.Request.ParseMultipartForm(12 << 20); err != nil {
		failure(c, http.StatusBadRequest, 40000, "音频上传失败，文件上限 12MB")
		return
	}
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		failure(c, http.StatusBadRequest, 40000, "请选择音频文件")
		return
	}
	defer file.Close()
	if strings.ToLower(filepath.Ext(header.Filename)) != ".wav" {
		failure(c, http.StatusBadRequest, 40000, "目前只支持 WAV 音频，请先将 MP3/M4A 等格式转换为 WAV")
		return
	}
	tmp, err := os.CreateTemp("", "xiaov2-manual-audio-*")
	if err != nil {
		writePlatformError(c, err)
		return
	}
	source := tmp.Name()
	defer os.Remove(source)
	if _, err = io.Copy(tmp, io.LimitReader(file, (12<<20)+1)); err != nil {
		_ = tmp.Close()
		writePlatformError(c, err)
		return
	}
	if err = tmp.Close(); err != nil {
		writePlatformError(c, err)
		return
	}
	if info, statErr := os.Stat(source); statErr != nil || info.Size() == 0 || info.Size() > 12<<20 {
		failure(c, http.StatusBadRequest, 40000, "音频文件为空或超过 12MB")
		return
	}
	version, err := strconv.ParseUint(c.PostForm("version"), 10, 64)
	if err != nil {
		failure(c, http.StatusBadRequest, 40000, "草稿版本无效")
		return
	}
	if err = h.service.UploadAudioItem(
		c.Request.Context(), c.Param("draftId"), p, c.Param("itemId"),
		c.PostForm("text"), source, version, identity(c).ID,
	); err != nil {
		writePlatformError(c, err)
		return
	}
	success(c, gin.H{"ok": true})
}

func (h *EditorHandler) RetryAudioIssue(c *gin.Context) {
	p, ok := pageParam(c)
	if !ok {
		return
	}
	var in struct {
		Accent  string `json:"accent"`
		ModelID string `json:"model_id"`
		VoiceID string `json:"voice_id"`
		Version uint64 `json:"version"`
	}
	if !bindJSON(c, &in) {
		return
	}
	if e := h.service.RetryAudioIssue(c.Request.Context(), c.Param("draftId"), p, c.Param("itemId"), in.Accent, in.ModelID, in.VoiceID, in.Version, identity(c).ID); e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"ok": true})
}
func (h *EditorHandler) ApproveAudioIssue(c *gin.Context) {
	p, ok := pageParam(c)
	if !ok {
		return
	}
	var in struct {
		Accent  string `json:"accent"`
		Version uint64 `json:"version"`
	}
	if !bindJSON(c, &in) {
		return
	}
	if e := h.service.ApproveAudioIssue(c.Request.Context(), c.Param("draftId"), p, c.Param("itemId"), in.Accent, in.Version, identity(c).ID); e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"ok": true})
}
func (h *EditorHandler) SaveMeta(c *gin.Context) {
	var in struct {
		Title           string `json:"title"`
		Grade           int    `json:"grade"`
		Term            string `json:"term"`
		Edition         string `json:"edition"`
		Version         uint64 `json:"version"`
		AmericanEnabled bool   `json:"american_enabled"`
		BritishEnabled  bool   `json:"british_enabled"`
		AmericanVoiceID string `json:"american_voice_id"`
		BritishVoiceID  string `json:"british_voice_id"`
	}
	if !bindJSON(c, &in) {
		return
	}
	if e := h.service.SaveMeta(c.Request.Context(), c.Param("draftId"), in.Title, in.Grade, in.Term, in.Edition, in.Version, identity(c).ID, service.AudioSettings{AmericanEnabled: in.AmericanEnabled, BritishEnabled: in.BritishEnabled, AmericanVoiceID: in.AmericanVoiceID, BritishVoiceID: in.BritishVoiceID}); e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"ok": true})
}

func (h *EditorHandler) SwitchModels(c *gin.Context) {
	var in struct {
		OCRModel         string `json:"ocr_model"`
		TranslationModel string `json:"translation_model"`
		TTSModel         string `json:"tts_model"`
		TTSVoice         string `json:"tts_voice"`
		Version          uint64 `json:"version"`
	}
	if !bindJSON(c, &in) {
		return
	}
	if e := h.service.SwitchModels(c.Request.Context(), c.Param("draftId"), in.Version, identity(c).ID, ai.Settings{OCRModel: in.OCRModel, TranslationModel: in.TranslationModel, TTSModel: in.TTSModel, TTSVoice: in.TTSVoice}); e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"ok": true})
}

func (h *EditorHandler) ModelOptions(c *gin.Context) { success(c, ai.Models()) }

func (h *EditorHandler) ModelVoices(c *gin.Context) {
	item, ok := ai.Find(c.Param("modelId"))
	if !ok || item.Type != "tts" {
		failure(c, http.StatusNotFound, 40400, "TTS 模型不存在")
		return
	}
	success(c, ai.Voices(item.ID))
}

func formBool(value string) bool {
	switch value {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}
func (h *EditorHandler) SavePage(c *gin.Context) {
	p, ok := pageParam(c)
	if !ok {
		return
	}
	var in service.DraftPageView
	if !bindJSON(c, &in) {
		return
	}
	if e := h.service.SavePage(c.Request.Context(), c.Param("draftId"), p, in, identity(c).ID); e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"ok": true})
}
func (h *EditorHandler) Order(c *gin.Context) {
	var in struct {
		Positions []int  `json:"positions"`
		Version   uint64 `json:"version"`
	}
	if !bindJSON(c, &in) {
		return
	}
	if e := h.service.Reorder(c.Request.Context(), c.Param("draftId"), in.Positions, in.Version, identity(c).ID); e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"ok": true})
}
func (h *EditorHandler) Action(c *gin.Context) {
	action := c.Param("action")
	need := "content.write"
	if action == "approve" || action == "reject" {
		need = "content.review"
	}
	if action == "publish" {
		need = "content.publish"
	}
	if !HasPermission(c, need) {
		failure(c, 403, 40300, "没有此操作的权限")
		return
	}
	var in struct {
		Version uint64 `json:"version"`
		Note    string `json:"note"`
		Page    int    `json:"page"`
	}
	if !bindJSON(c, &in) {
		return
	}
	if e := h.service.Action(c.Request.Context(), c.Param("draftId"), action, in.Note, in.Version, identity(c).ID, in.Page); e != nil {
		writePlatformError(c, e)
		return
	}
	success(c, gin.H{"ok": true})
}
func pageParam(c *gin.Context) (int, bool) {
	p, e := strconv.Atoi(c.Param("page"))
	if e != nil || p < 1 {
		failure(c, 400, 40002, "invalid page")
		return 0, false
	}
	return p, true
}
func serveFile(c *gin.Context, path, contentType string) {
	info, e := os.Stat(path)
	if e != nil || !info.Mode().IsRegular() {
		failure(c, 404, 40400, "资源不存在")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("Content-Type", contentType)
	c.File(path)
}
