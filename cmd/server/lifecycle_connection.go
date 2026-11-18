package main

import (
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

func serverLifecycleConnectionApplicationName(mode, buildSHA string) string {
	role := models.LifecycleWriterRole(mode)
	switch role {
	case models.LifecycleWriterRoleAPI, models.LifecycleWriterRoleWorker, models.LifecycleWriterRoleAll:
		return db.LifecycleConnectionApplicationName(role, buildSHA)
	default:
		// Preserve unsupported server modes; only the dedicated executor entry
		// point may identify itself as a session_executor process.
		return ""
	}
}
