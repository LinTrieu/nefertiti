package ethplorer

import (
	"strconv"

	"github.com/lintrieu/nefertiti/empty"
)

type (
	Top struct {
		*Token
		VolumeCurrent  interface{} `json:"volume-1d-current"`
		VolumePrevious interface{} `json:"volume-1d-previous"`
	}
)

func (top *Top) ParseVolumeCurrent() float64 {
	if out, err := strconv.ParseFloat(empty.AsString(top.VolumeCurrent), 64); err != nil {
		return out
	}
	return empty.AsFloat64(top.VolumeCurrent)
}

func (top *Top) ParseVolumePrevious() float64 {
	if out, err := strconv.ParseFloat(empty.AsString(top.VolumePrevious), 64); err != nil {
		return out
	}
	return empty.AsFloat64(top.VolumePrevious)
}

func (top *Top) VolumeDiff() float64 {
	volumeCurrent := top.ParseVolumeCurrent()
	if volumeCurrent > 0 {
		volumePrevious := top.ParseVolumePrevious()
		return ((volumeCurrent - volumePrevious) / volumePrevious) * 100
	}
	return 0
}

func (top *Top) Buy(volumeDiff float64) bool {
	return top.VolumeDiff() > volumeDiff && top.Price.Diff > 0
}
