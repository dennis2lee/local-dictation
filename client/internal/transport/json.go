package transport

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/coder/websocket"
)

// wsjson writes a value as one text frame.
//
// coder/websocket ships a wsjson helper, but it lives in a subpackage that
// would pull an extra import into every caller for what is four lines.
func wsjson(ctx context.Context, conn *websocket.Conn, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode %T: %w", value, err)
	}
	return conn.Write(ctx, websocket.MessageText, payload)
}
