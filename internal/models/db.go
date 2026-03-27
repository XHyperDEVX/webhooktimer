package models

import (
    "database/sql"
    _ "modernc.org/sqlite"
)

var DB *sql.DB

func InitDB(dataSourceName string) error {
    var err error
    DB, err = sql.Open("sqlite", dataSourceName)
    if err != nil {
        return err
    }

    _, err = DB.Exec(`
        CREATE TABLE IF NOT EXISTS timers (
            id TEXT PRIMARY KEY,
            name TEXT NOT NULL,
            webhook_url TEXT NOT NULL,
            mode TEXT NOT NULL,
            fixed_interval INTEGER,
            min_interval INTEGER,
            max_interval INTEGER,
            active BOOLEAN DEFAULT TRUE,
            last_execution DATETIME,
            webhook_timeout INTEGER DEFAULT 5,
            method TEXT DEFAULT 'POST'
        );
        CREATE TABLE IF NOT EXISTS logs (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            timer_id TEXT NOT NULL,
            timestamp DATETIME NOT NULL,
            status TEXT NOT NULL,
            message TEXT,
            FOREIGN KEY (timer_id) REFERENCES timers(id) ON DELETE CASCADE
        );
    `)
    return err
}
