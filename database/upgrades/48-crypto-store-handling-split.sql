-- v48: Move crypto/state/whatsmeow store upgrade handling to separate systems
CREATE TABLE crypto_version (version INTEGER PRIMARY KEY);
INSERT INTO crypto_version VALUES (6);
CREATE TABLE whatsmeow_version (version INTEGER PRIMARY KEY);
INSERT INTO whatsmeow_version VALUES (1);
CREATE TABLE mx_version (version INTEGER PRIMARY KEY);
INSERT INTO mx_version VALUES (1);
