package main

import "testing"

func TestGarbledSamples(t *testing.T) {
	// 用户实际案例：港台专辑"全 CJK 型误读"（编码信息已丢失，无法还原，
	// 但必须能被检测为乱码 → 乱码筛选可见 + 走刮削/识别补全）。
	garbled := []string{
		"浜洪潛鋆澞",
		"琛浣榈",
		"鐤孀祢铈疜澞缍揿吀",
		"姷甸珮鏄垛锛塲汱蹇橔级",
		"鋿富崙闽惽鋆",
		"璋佹槸璋侀潪", // UTF-8 字节被 GBK 误读（"谁是谁非"），可还原
		"椋為笩鍜岃潐", // 同上（"飞鸟和蝉"）
	}
	for _, s := range garbled {
		if !looksGarbledText(s) {
			t.Errorf("期望判乱码: %q", s)
		}
	}
	// 正常歌名/歌手/专辑不得误判为乱码。
	clean := []string{
		"开始恋爱", "陈慧娴", "吴雨霏", "下一站天后 (合唱版)", "Twins",
		"古巨基", "许美静", "薛之谦", "花粥", "习惯了寂寞", "孤单背影",
		"情深说话未曾讲", "明知故犯", "暧昧", "拍错拖", "我们总是在寻找",
		"牛奶@咖啡", "陈奕迅", "梁静茹", "最佳损友", "情歌", "十年", "勇气", "崇拜",
	}
	for _, s := range clean {
		if looksGarbledText(s) {
			t.Errorf("期望正常（不判乱码）: %q", s)
		}
	}
}
