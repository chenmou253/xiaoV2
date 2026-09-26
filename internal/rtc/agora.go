package rtc

import (
	"errors"
	"time"

	tokenbuilder "github.com/AgoraIO/Tools/DynamicKey/AgoraDynamicKey/go/src/rtctokenbuilder2"
)

type Agora struct{ AppID, Certificate string }

func (a Agora) Token(channel string, uid uint32, until time.Time) (string, error) {
	if a.AppID == "" || a.Certificate == "" {
		return "", errors.New("Agora RTC is not configured")
	}
	seconds := uint32(time.Until(until).Seconds())
	if seconds == 0 {
		return "", errors.New("lesson has ended")
	}
	return tokenbuilder.BuildTokenWithUid(a.AppID, a.Certificate, channel, uid, tokenbuilder.RolePublisher, seconds, seconds)
}
