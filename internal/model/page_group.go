package model

const (
	PageGroupCover    = "cover"
	PageGroupFront    = "front"
	PageGroupTitle    = "title"
	PageGroupContents = "contents"
	PageGroupBody     = "body"
	PageGroupAppendix = "appendix"
	PageGroupOther    = "other"
)

func ValidPageGroup(group string) bool {
	switch group {
	case "", PageGroupCover, PageGroupFront, PageGroupTitle, PageGroupContents, PageGroupBody, PageGroupAppendix, PageGroupOther:
		return true
	default:
		return false
	}
}
