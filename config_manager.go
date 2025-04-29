package main

import (
	"encoding/xml"
	"fmt"
	"io" // io.Reader用に追加
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings" // strings.NewReader用に追加
)

// --- ★ Stage 7 修正: より汎用的なXML構造体 ---
// server_config.xml の構造に合わせて調整が必要
// encoding/xml では属性と子要素を一つの struct で扱うのが難しい場合があるため、
// server_data の属性と子要素を分けて扱うか、カスタムマーシャラを検討する必要がある。
// ここでは、シンプルにするため属性のみを持つ構造体と、
// 子要素を含むかもしれない汎用構造体を定義してみる。

// <server_data> の属性部分
type ServerDataAttributes struct {
	XMLName xml.Name `xml:"server_data"` // これはマーシャル時には使われないが、アンマーシャル用に残す
	Port    string   `xml:"port,attr"`
	Name    string   `xml:"name,attr,omitempty"` // 他の属性も必要なら追加
	Seed    string   `xml:"seed,attr,omitempty"`
	// ... 他の属性 ...
}

// 汎用的なXML要素を表す構造体（再帰的に子要素を含む）
type xmlNode struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"` // 全ての属性を保持
	Content []byte     `xml:",innerxml"` // 子要素やテキスト内容はそのままバイト列で保持
}

/**
 * XML文字列内の server_data の port 属性を更新する (修正版)
 * @param {string} xmlString - 元のXML文字列
 * @param {number} newPort - 新しいポート番号
 * @returns {string} 更新されたXML文字列、またはエラー時空文字列
 * @throws {error} パース/エンコードエラー時
 */
func updateXmlPort(xmlString string, newPort int) (string, error) {
	decoder := xml.NewDecoder(strings.NewReader(xmlString))
	var updatedXml strings.Builder // 更新後のXMLを書き込むバッファ
	encoder := xml.NewEncoder(&updatedXml)
	encoder.Indent("", "  ") // インデントを設定

	newPortStr := strconv.Itoa(newPort)

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break // ファイル終端
		}
		if err != nil {
			log.Printf("[設定管理] エラー: XMLトークンの読み取り失敗: %v", err)
			return "", fmt.Errorf("XMLトークンの読み取り失敗: %w", err)
		}

		switch se := token.(type) {
		case xml.StartElement:
			if se.Name.Local == "server_data" {
				// <server_data> タグを見つけたら属性を処理
				updatedAttrs := []xml.Attr{}
				portAttrFound := false
				for _, attr := range se.Attr {
					if attr.Name.Local == "port" {
						// port 属性を新しい値で追加
						updatedAttrs = append(updatedAttrs, xml.Attr{Name: xml.Name{Local: "port"}, Value: newPortStr})
						portAttrFound = true
						log.Printf("[設定管理] XML内の port 属性を '%s' に更新します。", newPortStr)
					} else {
						// 他の属性はそのまま保持
						updatedAttrs = append(updatedAttrs, attr)
					}
				}
				// もし port 属性が元々なければ追加
				if !portAttrFound {
					updatedAttrs = append(updatedAttrs, xml.Attr{Name: xml.Name{Local: "port"}, Value: newPortStr})
					log.Printf("[設定管理] XMLに port 属性 '%s' を追加します。", newPortStr)
				}
				// 更新/追加された属性で開始タグをエンコード
				err = encoder.EncodeToken(xml.StartElement{Name: se.Name, Attr: updatedAttrs})
			} else {
				// <server_data> 以外の開始タグはそのままエンコード
				err = encoder.EncodeToken(token)
			}
		default:
			// 開始タグ以外 (終了タグ、テキストデータなど) はそのままエンコード
			err = encoder.EncodeToken(token)
		}

		if err != nil {
			log.Printf("[設定管理] エラー: XMLトークンの書き込み失敗: %v", err)
			return "", fmt.Errorf("XMLトークンの書き込み失敗: %w", err)
		}
	}

	// エンコーダーのバッファをフラッシュ
	if err := encoder.Flush(); err != nil {
		log.Printf("[設定管理] エラー: XMLエンコーダーのフラッシュ失敗: %v", err)
		return "", fmt.Errorf("XMLエンコーダーのフラッシュ失敗: %w", err)
	}

	finalXml := updatedXml.String()
	log.Printf("[設定管理] ポート更新後のXML生成完了。")
	// log.Printf("[設定管理] 更新後XML(一部):\n%s", finalXml[:min(500, len(finalXml))]) // デバッグ用

	return finalXml, nil
}

// min 関数の定義 (Go 1.21 未満の場合)
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}


/**
 * 設定ファイルを保存する (修正版)
 * @param {string} configName - 構成名
 * @param {string} xmlString - 保存するXML文字列
 * @returns {error} エラー、または成功時 nil
 */
func saveConfigFile(configName string, xmlString string) error {
	configDir := filepath.Join(configBaseDir, configName)
	configFilePath := filepath.Join(configDir, "server_config.xml")

	// ★ 絶対パスをログに出力
	absConfigFilePath, pathErr := filepath.Abs(configFilePath)
	if pathErr != nil {
		log.Printf("[設定管理] 警告: 設定ファイルの絶対パス取得に失敗 (%s): %v", configFilePath, pathErr)
		absConfigFilePath = configFilePath // 相対パスのままログに出す
	}
	log.Printf("[設定管理] 設定ファイルを保存します: %s", absConfigFilePath)

	// ディレクトリ作成
	if err := os.MkdirAll(configDir, 0755); err != nil {
		log.Printf("[設定管理] エラー: 設定ディレクトリ作成失敗 (%s): %v", configDir, err)
		return fmt.Errorf("設定ディレクトリ作成失敗: %w", err)
	}

	// ファイル書き込み
	if err := os.WriteFile(absConfigFilePath, []byte(xmlString), 0644); err != nil {
		// ★ エラーを詳細に出力
		log.Printf("[設定管理] エラー: 設定ファイル書き込み失敗 (%s): %v", absConfigFilePath, err)
		return fmt.Errorf("設定ファイル書き込み失敗 (%s): %w", absConfigFilePath, err)
	}

	log.Printf("[設定管理] 設定ファイル保存成功: %s", absConfigFilePath)
	return nil
}