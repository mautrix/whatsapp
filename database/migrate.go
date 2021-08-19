package database

import (
	"fmt"
	"math"
	"strings"
)

func countRows(db *Database, table string) (int, error) {
	countRow := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table))
	var count int
	err := countRow.Scan(&count)
	return count, err
}

const VariableCountLimit = 512

func migrateTable(old *Database, new *Database, table string, columns ...string) error {
	columnNames := strings.Join(columns, ",")
	fmt.Printf("Migrating %s: ", table)
	rowCount, err := countRows(old, table)
	if err != nil {
		return err
	}
	fmt.Print("found ", rowCount, " rows of data, ")
	rows, err := old.Query(fmt.Sprintf("SELECT %s FROM \"%s\"", columnNames, table))
	if err != nil {
		return err
	}
	serverColNames, err := rows.Columns()
	if err != nil {
		return err
	}
	colCount := len(serverColNames)
	valueStringFormat := strings.Repeat("$%d, ", colCount)
	valueStringFormat = fmt.Sprintf("(%s)", valueStringFormat[:len(valueStringFormat)-2])
	cols := make([]interface{}, colCount)
	colPtrs := make([]interface{}, colCount)
	for i := 0; i < colCount; i++ {
		colPtrs[i] = &cols[i]
	}
	batchSize := VariableCountLimit / colCount
	values := make([]interface{}, batchSize*colCount)
	valueStrings := make([]string, batchSize)
	var inserted int64
	batchCount := int(math.Ceil(float64(rowCount) / float64(batchSize)))
	tx, err := new.Begin()
	if err != nil {
		return err
	}
	fmt.Printf("migrating in %d batches: ", batchCount)
	for rowCount > 0 {
		var i int
		for ; rows.Next() && i < batchSize; i++ {
			colPtrs := make([]interface{}, colCount)
			valueStringArgs := make([]interface{}, colCount)
			for j := 0; j < colCount; j++ {
				pos := i*colCount + j
				colPtrs[j] = &values[pos]
				valueStringArgs[j] = pos + 1
			}
			valueStrings[i] = fmt.Sprintf(valueStringFormat, valueStringArgs...)
			err = rows.Scan(colPtrs...)
			if err != nil {
				panic(err)
			}
		}
		slicedValues := values
		slicedValueStrings := valueStrings
		if i < len(valueStrings) {
			slicedValueStrings = slicedValueStrings[:i]
			slicedValues = slicedValues[:i*colCount]
		}
		if len(slicedValues) == 0 {
			break
		}
		res, err := tx.Exec(fmt.Sprintf("INSERT INTO \"%s\" (%s) VALUES %s", table, columnNames, strings.Join(slicedValueStrings, ",")), slicedValues...)
		if err != nil {
			panic(err)
		}
		count, _ := res.RowsAffected()
		inserted += count
		rowCount -= batchSize
		fmt.Print("#")
	}
	err = tx.Commit()
	if err != nil {
		return err
	}
	fmt.Println(" -- done with", inserted, "rows inserted")
	return nil
}

func Migrate(old *Database, new *Database) {
	err := migrateTable(old, new, "portal", "jid", "receiver", "mxid", "name", "topic", "avatar", "avatar_url", "encrypted")
	if err != nil {
		panic(err)
	}
	err = migrateTable(old, new, "user", "mxid", "jid", "management_room", "client_id", "client_token", "server_token", "enc_key", "mac_key", "last_connection")
	if err != nil {
		panic(err)
	}
	err = migrateTable(old, new, "puppet", "jid", "avatar", "displayname", "name_quality", "custom_mxid", "access_token", "next_batch", "avatar_url", "enable_presence", "enable_receipts")
	if err != nil {
		panic(err)
	}
	err = migrateTable(old, new, "user_portal", "user_jid", "portal_jid", "portal_receiver", "in_community")
	if err != nil {
		panic(err)
	}
	err = migrateTable(old, new, "message", "chat_jid", "chat_receiver", "jid", "mxid", "sender", "timestamp", "sent")
	if err != nil {
		panic(err)
	}
	err = migrateTable(old, new, "mx_registrations", "user_id")
	if err != nil {
		panic(err)
	}
	err = migrateTable(old, new, "mx_user_profile", "room_id", "user_id", "membership", "displayname", "avatar_url")
	if err != nil {
		panic(err)
	}
	err = migrateTable(old, new, "mx_room_state", "room_id", "power_levels")
	if err != nil {
		panic(err)
	}
	err = migrateTable(old, new, "crypto_account", "account_id", "device_id", "shared", "sync_token", "account")
	if err != nil {
		panic(err)
	}
	err = migrateTable(old, new, "crypto_message_index", "sender_key", "session_id", `"index"`, "event_id", "timestamp")
	if err != nil {
		panic(err)
	}
	err = migrateTable(old, new, "crypto_tracked_user", "user_id")
	if err != nil {
		panic(err)
	}
	err = migrateTable(old, new, "crypto_device", "user_id", "device_id", "identity_key", "signing_key", "trust", "deleted", "name")
	if err != nil {
		panic(err)
	}
	err = migrateTable(old, new, "crypto_olm_session", "account_id", "session_id", "sender_key", "session", "created_at", "last_used")
	if err != nil {
		panic(err)
	}
	err = migrateTable(old, new, "crypto_megolm_inbound_session", "account_id", "session_id", "sender_key", "signing_key", "room_id", "session", "forwarding_chains", "withheld_code", "withheld_reason")
	if err != nil {
		panic(err)
	}
	err = migrateTable(old, new, "crypto_megolm_outbound_session", "account_id", "room_id", "session_id", "session", "shared", "max_messages", "message_count", "max_age", "created_at", "last_used")
	if err != nil {
		panic(err)
	}
}
