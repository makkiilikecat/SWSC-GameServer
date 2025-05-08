package main

import (
	"fmt"
	"log"
	"sync"
)

var (
	usedPorts      map[int]bool // 使用中のポートを管理 (キー: ポート番号, 値: true)
	usedPortsMutex sync.Mutex   // マップアクセス保護用
)

// ポートマネージャー初期化
func initializePortManager() {
	usedPorts = make(map[int]bool)
	log.Println("[ポート管理] ポートマネージャーを初期化しました。")
}

// 指定範囲内で利用可能なポートを探す
func findAvailablePort(minPort, maxPort int) (int, error) {
	usedPortsMutex.Lock()
	defer usedPortsMutex.Unlock()

	for port := minPort; port <= maxPort; port++ {
		if !usedPorts[port] { // マップに存在しない = 未使用
			log.Printf("[ポート管理] 空きポート発見: %d", port)
			return port, nil
		}
	}
	log.Printf("[ポート管理] エラー: 利用可能なポートが範囲内 (%d-%d) に見つかりません。", minPort, maxPort)
	return -1, fmt.Errorf("利用可能なポートがありません (%d-%d)", minPort, maxPort)
}

// ポートを使用中にマークする
func assignPort(port int) bool {
	if port < MinPort || port > MaxPort { // config.go の変数を使用
		log.Printf("[ポート管理] 警告: 範囲外のポート %d を使用中にマークしようとしました。", port)
		return false
	}
	usedPortsMutex.Lock()
	defer usedPortsMutex.Unlock()

	if usedPorts[port] {
		log.Printf("[ポート管理] 警告: ポート %d は既に使用中です。", port)
		return false // すでに使用中
	}
	usedPorts[port] = true
	log.Printf("[ポート管理] ポート %d を使用中にマークしました。", port)
	return true
}

// ポートを解放する
func releasePort(port int) {
	if port < MinPort || port > MaxPort {
		log.Printf("[ポート管理] 警告: 範囲外のポート %d を解放しようとしました。", port)
		return
	}
	usedPortsMutex.Lock()
	defer usedPortsMutex.Unlock()

	if !usedPorts[port] {
		log.Printf("[ポート管理] 警告: 未使用のポート %d を解放しようとしました。", port)
	} else {
		delete(usedPorts, port) // マップから削除
		log.Printf("[ポート管理] ポート %d を解放しました。", port)
	}
}

// 現在使用中のポートリストを取得 (デバッグ用など)
func getCurrentlyUsedPorts() []int {
    usedPortsMutex.Lock()
    defer usedPortsMutex.Unlock()
    ports := make([]int, 0, len(usedPorts))
    for port := range usedPorts {
        ports = append(ports, port)
    }
    return ports
}