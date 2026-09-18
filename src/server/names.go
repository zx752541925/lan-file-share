package main

import (
	"fmt"
	"math/rand/v2"
	"regexp"
)

const hostName = "主机"

// 访客默认昵称：词库 + 两位数字，数字在同时连接的设备之间不重复
var guestWords = []string{
	"海豚", "青柠", "松鼠", "流星", "雪豹", "柚子", "山雀", "竹叶",
	"橘子", "风铃", "蓝鲸", "松果", "蜜桃", "北极熊", "游鱼", "云雀",
	"银杏", "珊瑚", "星河", "麦穗",
}

var guestNumberPattern = regexp.MustCompile(`-(\d{1,3})$`)

// randomGuestName 取一个没被占用的数字，凑成「海豚-27」这样的名字。
func randomGuestName(taken func(int) bool) (string, int) {
	word := guestWords[rand.IntN(len(guestWords))]
	for i := 0; i < 300; i++ {
		number := rand.IntN(98) + 1 // 1 - 98
		if !taken(number) {
			return fmt.Sprintf("%s-%02d", word, number), number
		}
	}

	number := rand.IntN(98) + 1
	return fmt.Sprintf("%s-%02d", word, number), number
}

// numberFromName 从「海豚-27」里取出 27，取不到返回 0。
func numberFromName(name string) int {
	match := guestNumberPattern.FindStringSubmatch(name)
	if match == nil {
		return 0
	}

	number := 0
	if _, err := fmt.Sscanf(match[1], "%d", &number); err != nil {
		return 0
	}
	return number
}
