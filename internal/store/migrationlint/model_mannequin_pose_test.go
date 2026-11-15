package migrationlint

import (
	"strings"
	"testing"
)

func TestModelMannequinPoseMigrationOwnsVersionedResource(t *testing.T) {
	body := readMigrationFile(t, "0469_model_mannequin_pose.sql")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS model_mannequin_pose",
		"PRIMARY KEY (model_id)",
		"FOREIGN KEY (model_id) REFERENCES model(id) ON DELETE CASCADE",
		"payload JSON NOT NULL",
		"canonical_sha256 CHAR(64)",
		"revision BIGINT UNSIGNED",
		"updated_by VARCHAR(255) NOT NULL",
		"CONSTRAINT chk_model_mannequin_pose_schema",
		"CONSTRAINT chk_model_mannequin_pose_hash",
		"DROP TABLE IF EXISTS model_mannequin_pose",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("0469 mannequin pose migration is missing %q", required)
		}
	}
	if strings.Contains(body, "ALTER TABLE model ") || strings.Contains(body, "comment") && strings.Contains(body, "UPDATE model") {
		t.Error("pose migration must stay a separate resource and must not repurpose model/comment")
	}
}
