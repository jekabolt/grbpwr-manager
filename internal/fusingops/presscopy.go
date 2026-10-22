package fusingops

import "github.com/jekabolt/grbpwr-manager/internal/entity"

// PressCopyColumns are the tech_card_operation columns a per-piece step split off a grouped step
// inherits from it: how and where the press runs, nothing about the step's own content (number,
// note, media, SMV, links). The store copies exactly these columns with INSERT … SELECT; the write
// model copies the same fields with CopyPressSettings. A test pins the two lists together.
var PressCopyColumns = []string{
	"zone", "press_equipment", "press_profile_key", "press_temperature_c", "press_dwell_sec",
	"press_pressure_n_cm2", "press_steam", "press_cloth", "work",
}

// CopyPressSettings copies the PressCopyColumns fields of src onto dst.
func CopyPressSettings(dst, src *entity.TechCardOperation) {
	dst.Zone = src.Zone
	dst.PressEquipment = src.PressEquipment
	dst.PressProfileKey = src.PressProfileKey
	dst.PressTemperatureC = src.PressTemperatureC
	dst.PressDwellSec = src.PressDwellSec
	dst.PressPressureNCm2 = src.PressPressureNCm2
	dst.PressSteam = src.PressSteam
	dst.PressCloth = src.PressCloth
	dst.Work = src.Work
}
