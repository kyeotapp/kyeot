package disk

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/sensor/text/texttest"
)

func TestTextKeysMatchKorean(t *testing.T) {
	for _, b := range []Bundle{
		Build(Usage{Used: 1e9, Avail: 3e9}, nil, time.Now()),
		Build(Usage{}, errors.New("permission denied"), time.Now()),
		Build(Usage{}, nil, time.Now()),
	} {
		raw, _ := json.Marshal(b)
		texttest.Check(t, raw)
	}
}
