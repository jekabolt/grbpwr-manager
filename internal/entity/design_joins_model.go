package entity

import (
	"encoding/json"
	"strconv"
	"strings"
)

// DesignJoinsAnswer — the JSON the join-list model answers with (r5.py's schema + layers + the
// photos verdict), read leniently: numbers may come as strings, the older verdict words
// (same_garment / notes / trust) are accepted beside the new ones.
type DesignJoinsAnswer struct {
	Consistency struct {
		Consistent  *bool  `json:"consistent"`
		SameGarment *bool  `json:"same_garment"`
		Note        string `json:"note"`
		Notes       string `json:"notes"`
		Groups      []struct {
			Images []json.RawMessage `json:"images"`
			What   string            `json:"what"`
		} `json:"groups"`
		Keep  []json.RawMessage `json:"keep"`
		Trust []json.RawMessage `json:"trust"`
	} `json:"consistency"`
	Layers []struct {
		Index json.RawMessage `json:"index"`
		Name  string          `json:"name"`
		Sheer bool            `json:"sheer"`
		Note  string          `json:"note"`
		Face  string          `json:"face"`
	} `json:"layers"`
	Items []struct {
		ID            string          `json:"id"`
		Kind          string          `json:"kind"`
		Path          []string        `json:"path"`
		Anchor        string          `json:"anchor"`
		Closed        bool            `json:"closed"`
		Width         string          `json:"width"`
		Type          string          `json:"type"`
		Count         json.RawMessage `json:"count"`
		BoundedBy     []string        `json:"bounded_by"`
		ContinuesInto []string        `json:"continues_into"`
		Note          string          `json:"note"`
		Layer         json.RawMessage `json:"layer"`
		Visibility    string          `json:"visibility"`
		CaughtInto    []string        `json:"caught_into"`
		FreeEdge      bool            `json:"free_edge"`
		Sharp         []string        `json:"sharp"`
	} `json:"items"`
	Absent    []string `json:"absent"`
	Absences  []string `json:"absences"`
	Uncertain []string `json:"uncertain"`
}

// ParseDesignJoinsAnswer finds the {"items": …} object in a model's answer: bare, fenced or wrapped
// in prose. False when there is none.
func ParseDesignJoinsAnswer(raw string) (DesignJoinsAnswer, bool) {
	try := func(s string) (DesignJoinsAnswer, bool) {
		var probe map[string]json.RawMessage
		if json.Unmarshal([]byte(s), &probe) != nil {
			return DesignJoinsAnswer{}, false
		}
		if _, has := probe["items"]; !has {
			return DesignJoinsAnswer{}, false
		}
		var out DesignJoinsAnswer
		if json.Unmarshal([]byte(s), &out) != nil {
			return DesignJoinsAnswer{}, false
		}
		return out, true
	}
	body := strings.TrimSpace(raw)
	if out, ok := try(body); ok {
		return out, true
	}
	if i, j := strings.Index(body, "{"), strings.LastIndex(body, "}"); i >= 0 && j > i {
		return try(body[i : j+1])
	}
	return DesignJoinsAnswer{}, false
}

// designJoinsInt reads a number that may come as a string; 0 when it is neither.
func designJoinsInt(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return int(f)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		n, _ := strconv.Atoi(strings.TrimSpace(s))
		return n
	}
	return 0
}

// Doc — the answer as a join list, CLEANED (SanitizeDesignJoinsDoc).
func (a DesignJoinsAnswer) Doc() DesignJoinsDoc {
	var d DesignJoinsDoc
	for _, l := range a.Layers {
		d.Layers = append(d.Layers, DesignJoinLayer{Index: designJoinsInt(l.Index), Name: l.Name, Sheer: l.Sheer, Note: l.Note, Face: l.Face})
	}
	for _, it := range a.Items {
		c := DesignJoinItem{
			ID: it.ID, Kind: it.Kind, Closed: it.Closed, Width: it.Width, Type: it.Type,
			Count: designJoinsInt(it.Count), BoundedBy: it.BoundedBy, ContinuesInto: it.ContinuesInto,
			Text: it.Note, Layer: designJoinsInt(it.Layer), Visibility: it.Visibility,
			CaughtInto: it.CaughtInto, FreeEdge: it.FreeEdge, Sharp: it.Sharp,
		}
		switch {
		case strings.EqualFold(strings.TrimSpace(it.Kind), DesignJoinKindPocket):
			c.From = it.Anchor
			if c.From == "" && len(it.Path) > 0 {
				c.From = it.Path[0]
			}
		case len(it.Path) >= 2:
			c.From, c.To = it.Path[0], it.Path[len(it.Path)-1]
			c.Via = append([]string(nil), it.Path[1:len(it.Path)-1]...)
		case len(it.Path) == 1:
			c.From = it.Path[0]
		}
		d.Items = append(d.Items, c)
	}
	d.Absences = append(append([]string(nil), a.Absent...), a.Absences...)
	d.Uncertain = a.Uncertain
	return SanitizeDesignJoinsDoc(d)
}

// ConsistencyFor — the photos verdict with image numbers (1-based, in the order the photos travelled)
// turned into media ids, cleaned against them.
func (a DesignJoinsAnswer) ConsistencyFor(mediaIDs []int) DesignJoinsConsistency {
	idOf := func(raw json.RawMessage) int {
		n := designJoinsInt(raw)
		if n < 1 || n > len(mediaIDs) {
			return 0
		}
		return mediaIDs[n-1]
	}
	c := a.Consistency
	out := DesignJoinsConsistency{Consistent: true}
	switch {
	case c.Consistent != nil:
		out.Consistent = *c.Consistent
	case c.SameGarment != nil:
		out.Consistent = *c.SameGarment
	}
	out.Note = c.Note
	if out.Note == "" {
		out.Note = c.Notes
	}
	keep := c.Keep
	if len(keep) == 0 {
		keep = c.Trust
	}
	for _, k := range keep {
		if id := idOf(k); id > 0 {
			out.KeepMediaIDs = append(out.KeepMediaIDs, id)
		}
	}
	for _, g := range c.Groups {
		var ids []int
		for _, k := range g.Images {
			if id := idOf(k); id > 0 {
				ids = append(ids, id)
			}
		}
		out.Groups = append(out.Groups, DesignJoinsGroup{MediaIDs: ids, What: g.What})
	}
	return SanitizeDesignJoinsConsistency(out, mediaIDs)
}
