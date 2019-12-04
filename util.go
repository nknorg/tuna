package tuna

import (
	"strings"

	"github.com/nknorg/nkn/common"
)

func ParsePrice(priceStr string) (common.Fixed64, common.Fixed64, error) {
	price := strings.Split(priceStr, ",")
	entryToExitPrice, err := common.StringToFixed64(strings.Trim(price[0], " "))
	if err != nil {
		return 0, 0, err
	}
	var exitToEntryPrice common.Fixed64
	if len(price) > 1 {
		exitToEntryPrice, err = common.StringToFixed64(strings.Trim(price[1], " "))
		if err != nil {
			return 0, 0, err
		}
	} else {
		exitToEntryPrice = entryToExitPrice
	}
	return entryToExitPrice, exitToEntryPrice, nil
}
