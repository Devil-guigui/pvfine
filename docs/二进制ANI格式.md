# 通用二进制 ANI 解码

实现位于 `internal/ani/binary.go`，独立于 PVF 容器与客户端版本。编辑器通过公共文件读取流程展示二进制 `.ani` 的只读源码，并接入现有 ANI 动画预览。普通文本 ANI 和令牌脚本 ANI 沿用原有流程。

当前支持解码，不包含文本到二进制的重编译。查看与预览不会改变文件类型、原始载荷或归档的修改状态，导出及归档保存继续使用原始二进制。无法解码的文件显示错误；不能识别的属性不会被跳过。

## 布局

所有数值按小端读取。

1. `uint16` 帧数、`uint16` 图片路径数。
2. 每条图片路径：`int32` 字节长度，随后为 ASCII 字节。
3. `uint16` 全局属性数量，随后为属性编号与对应数据。
4. 每一帧：碰撞盒数量及盒数据、`int16` 图片路径索引、可选的 `uint16` 图片编号、两个 `int32` 图片坐标、帧属性数量及属性数据。

负图片路径索引表示空图片，不占图片编号字段。碰撞盒含六个 `int32`。解码器检查长度、索引、属性编号和尾部数据，最多接受三个零对齐字节；限制单个资源字符串和解码输出大小，防止异常计数或路径复用导致过大内存占用。

支持的全局属性为 LOOP、SHADOW、COORD、OPERATION 和 SPECTRUM（间隔、寿命、颜色、效果）。帧属性包括 LOOP、SHADOW、COORD、IMAGE RATE、IMAGE ROTATE、RGBA、INTERPOLATION、GRAPHIC EFFECT、DELAY、DAMAGE TYPE、PLAY SOUND、PRELOAD、SET FLAG、FLIP TYPE、LOOP START、LOOP END、CLIP，以及 DAMAGE BOX / ATTACK BOX。MONOCHROME 和 SPACEDISTORT 的附加参数也会读取。

源码使用 `[FRAME000]`、`[IMAGE]`、`[IMAGE POS]` 等标准 ANI Section。帧内属性缩进，避免帧级 LOOP / SHADOW 被预览解析器误认为全局设置。现有预览显示图片、位置和延时等已支持信息；完整属性保留在解码文本中，并不表示预览器已模拟所有特效。

## 参考与验证

格式依据 `pvfUtility` 提交 `53dc8e48bba0f7871ca96bcf10bfe6d2028d0582` 的读取逻辑及本地样本验证，Go 实现独立编写，未复制其恢复源码。上游 README 明确说明没有统一的顶层许可证。

- [BinaryAniCompiler.cs](https://github.com/Lancarus/pvfUtility/blob/53dc8e48bba0f7871ca96bcf10bfe6d2028d0582/SourceLibraries/PvfCode.Services/PvfCode/Services/BinaryAniCompiler.cs#L166)
- [格式枚举](https://github.com/Lancarus/pvfUtility/tree/53dc8e48bba0f7871ca96bcf10bfe6d2028d0582/SourceLibraries/PvfCode.NPK.Utils/PvfCode/NPK/Utils/AniModel/Enums)
- [上游来源与许可说明](https://github.com/Lancarus/pvfUtility/blob/53dc8e48bba0f7871ca96bcf10bfe6d2028d0582/README.md#来源与许可说明)

回归覆盖所有已知属性、负坐标、浮点数、空图片、字符串转义、未知枚举值、每个截断位置、异常计数/索引/属性、对齐和异常尾部、标准容器中的读取及保存不变、编辑器只读状态与动画预览。

```sh
go test ./internal/ani ./internal/preview ./internal/pvf ./services
PVF_TESTFILE=/path/to/Script.pvf go test ./internal/pvf -run '^TestRealBinaryANIDecode$' -v -count=1
```

本地样本验证：A5 全部 74,688 个、A12 全部 84,804 个原生二进制 ANI 解码成功。A4 样本不含原生二进制 ANI。
