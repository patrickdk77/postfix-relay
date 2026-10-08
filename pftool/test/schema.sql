CREATE DATABASE IF NOT EXISTS ysmaster;
USE ysmaster;
CREATE TABLE mail_sender_policy (
  sender varchar(255) NOT NULL PRIMARY KEY,
  policy varchar(255) DEFAULT NULL,
  status tinyint NOT NULL DEFAULT 0,
  hash varchar(64) DEFAULT NULL,
  lastupdate datetime DEFAULT NULL
);
INSERT INTO mail_sender_policy VALUES
  ('alice@spam.example', 'HOLD', 0, NULL, NOW()),
  ('dave@other.example', 'REJECT Account used for sending spam', 2, 'davehash', NOW()),
  ('erin.example', 'dunno', 3, 'erinhash', NOW()),
  ('stale@old.example', 'HOLD', 1, 'stalehash', DATE_SUB(NOW(), INTERVAL 2 HOUR));
