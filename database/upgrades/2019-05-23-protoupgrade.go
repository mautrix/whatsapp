package upgrades

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

func init() {
	var keys = []string{"imageMessage", "contactMessage", "locationMessage", "extendedTextMessage", "documentMessage", "audioMessage", "videoMessage"}
	upgrades[4] = upgrade{"Update message content to new protocol version. This may take a while.", func(tx *sql.Tx, ctx context) error {
		rows, err := ctx.db.Query("SELECT mxid, content FROM message")
		if err != nil {
			return err
		}
		for rows.Next() {
			var mxid string
			var rawContent []byte
			err = rows.Scan(&mxid, &rawContent)
			if err != nil {
				fmt.Println("Error scanning:", err)
				continue
			}
			var content map[string]interface{}
			err = json.Unmarshal(rawContent, &content)
			if err != nil {
				fmt.Printf("Error unmarshaling content of %s: %v\n", mxid, err)
				continue
			}

			for _, key := range keys {
				val, ok := content[key].(map[string]interface{})
				if !ok {
					continue
				}
				ci, ok := val["contextInfo"].(map[string]interface{})
				if !ok {
					continue
				}
				qm, ok := ci["quotedMessage"].([]interface{})
				if !ok {
					continue
				}
				ci["quotedMessage"] = qm[0]
				goto save
			}
			continue

		save:
			rawContent, err = json.Marshal(&content)
			if err != nil {
				fmt.Printf("Error marshaling updated content of %s: %v\n", mxid, err)
			}
			_, err = tx.Exec("UPDATE message SET content=$1 WHERE mxid=$2", rawContent, mxid)
			if err != nil {
				fmt.Printf("Error updating row of %s: %v\n", mxid, err)
			}
		}
		return nil
	}}
}
