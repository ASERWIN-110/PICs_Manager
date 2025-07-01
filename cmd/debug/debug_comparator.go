//go:build ignore
// +build ignore

// ^^^ 在运行或编译前，请注释掉上面两行

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
)

func main() {
	log.Println("========================================")
	log.Println("===     文件内容比较与哈希诊断工具     ===")
	log.Println("========================================")

	// 1. 检查命令行参数
	if len(os.Args) != 3 {
		log.Fatalf("错误: 需要提供两个文件路径作为参数。\n用法: .\\comparator.exe <文件路径1> <文件路径2>")
	}

	filePath1 := os.Args[1]
	filePath2 := os.Args[2]

	log.Printf("正在比较文件A和文件B:\n  - 文件A: %s\n  - 文件B: %s\n", filePath1, filePath2)

	// 2. 分别计算两个文件的哈希值
	hash1, err1 := calculateSHA256(filePath1)
	if err1 != nil {
		log.Fatalf("错误：计算文件A的哈希失败: %v", err1)
	}
	hash2, err2 := calculateSHA256(filePath2)
	if err2 != nil {
		log.Fatalf("错误：计算文件B的哈希失败: %v", err2)
	}

	fmt.Println("\n--- 哈希计算结果 ---")
	fmt.Printf("文件A的SHA-256: %s\n", hash1)
	fmt.Printf("文件B的SHA-256: %s\n", hash2)

	if hash1 == hash2 {
		fmt.Println("结论: ✅ 两个文件的SHA-256哈希值完全相同。")
	} else {
		fmt.Println("结论: ❌ 两个文件的SHA-256哈希值不同。")
	}

	// 3. 进行逐字节的二进制内容比较
	fmt.Println("\n--- 二进制内容比较结果 ---")
	areIdentical, err := compareFileBytes(filePath1, filePath2)
	if err != nil {
		log.Fatalf("比较文件内容时出错: %v", err)
	}

	if areIdentical {
		fmt.Println("结论: ✅ 两个文件的二进制内容完全相同。")
	} else {
		fmt.Println("结论: ❌ 两个文件的二进制内容不同。")
	}

	fmt.Println("\n--- 最终诊断 ---")
	if (hash1 == hash2) && areIdentical {
		fmt.Println("诊断结果: 一切正常。这两个文件在字节层面完全一样，因此产生相同的哈希是预期的、正确的行为。")
	} else if (hash1 != hash2) && !areIdentical {
		fmt.Println("诊断结果: 一切正常。这两个文件在字节层面不同，因此产生不同的哈希是预期的、正确的行为。")
	} else {
		fmt.Println("!!! 严重警告 !!! 出现了理论上不可能的情况（哈希相同但内容不同，或哈希不同但内容相同）。")
		fmt.Println("这可能意味着您的硬件（内存或硬盘）存在问题，或者Go语言的哈希库在您的系统上出现了罕见的Bug。")
	}
}

// calculateSHA256 是一个独立的哈希计算函数
func calculateSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// compareFileBytes 逐字节比较两个文件
func compareFileBytes(file1, file2 string) (bool, error) {
	const chunkSize = 64 * 1024 // 64KB
	f1, err := os.Open(file1)
	if err != nil {
		return false, err
	}
	defer f1.Close()
	f2, err := os.Open(file2)
	if err != nil {
		return false, err
	}
	defer f2.Close()

	for {
		b1 := make([]byte, chunkSize)
		_, err1 := f1.Read(b1)
		b2 := make([]byte, chunkSize)
		_, err2 := f2.Read(b2)

		if err1 == io.EOF && err2 == io.EOF {
			return true, nil
		}
		if err1 != nil || err2 != nil {
			if err1 == io.EOF || err2 == io.EOF { // 文件长度不同
				return false, nil
			}
			return false, fmt.Errorf("读取文件时出错: %v / %v", err1, err2)
		}

		if !bytes.Equal(b1, b2) {
			return false, nil
		}
	}
}
