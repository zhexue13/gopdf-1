package gopdf

import (
	"math"

	"github.com/tiechui1994/gopdf/core"
)

/**
Table写入的实现思路:
	先构建一个标准table(n*m),
	然后在标准的table基础上构建不规则的table.

标准table: (4*5)
+-----+-----+-----+-----+-----+
|(0,0)|(0,1)|(0,2)|(0,3)|(0,4)|
+-----+-----+-----+-----+-----+
|(1,0)|(1,1)|(1,2)|(1,3)|(1,4)|
+-----+-----+-----+-----+-----+
|(2,0)|(2,1)|(2,2)|(2,3)|(2,4)|
+-----+-----+-----+-----+-----+
|(3,0)|(3,1)|(3,2)|(3,3)|(3,4)|
+-----+-----+-----+-----+-----+

不规则table: 在 (4*5) 基础上构建
+-----+-----+-----+-----+-----+
|(0,0)      |(0,2)      |(0,4)|
+	        +-----+-----+     +
|           |(1,2)      |     |
+-----+-----+-----+-----+     +
|(2,0)            |(2,3)|     |
+                 +     +-----+
|                 |     |(3,4)|
+-----+-----+-----+-----+-----+

在创建不规则table的时候, 先构建标准table, 然后再 "描述" 不规则table,
一旦不规则 table 描述完毕之后, 其余的工作交由程序, 自动分页, 换行去生成
描述的不规则table.

Table主要负载的是生成最终表格.
core.Cell接口的实现类可以生成自定义的单元格. 默认的一个实现是TextCell, 基于纯文本的Cell

注: 目前在table的分页当中, 背景颜色和线条存在bug.
**/

// 构建表格
type Table struct {
	pdf           *core.Report
	rows, cols    int // 行数,列数
	width, height float64
	colwidths     []float64      // 列宽百分比: 应加起来为1
	rowheights    []float64      // 保存行高
	cells         [][]*TableCell // 单元格

	lineHeight float64    // 默认行高
	margin     core.Scope // 位置调整

	nextrow, nextcol int // 下一个位置
	hasWrited        int // 当前页面可以 "完整" 写入的行数. 完整的含义是用户自定义cell的对齐

	tableCheck bool      // table 完整性检查
	cachedRow  []float64 // 缓存行
	cachedCol  []float64 // 缓存列
}

type TableCell struct {
	table            *Table // table元素
	row, col         int    // 位置
	rowspan, colspan int    // 单元格大小

	element    core.Cell // 单元格元素
	minheight  float64   // 当前最小单元格的高度, rowspan=1, 辅助计算
	height     float64   // 当前表格单元真实高度, rowspan >= 1, 实际计算垂直线高度的时候使用
	cellwrited int
}

func (cell *TableCell) SetElement(e core.Cell) *TableCell {
	cell.element = e
	return cell
}

func NewTable(cols, rows int, width, lineHeight float64, pdf *core.Report) *Table {
	contentWidth, _ := pdf.GetContentWidthAndHeight()
	if width > contentWidth {
		width = contentWidth
	}

	t := &Table{
		pdf:    pdf,
		rows:   rows,
		cols:   cols,
		width:  width,
		height: 0,

		nextcol: 0,
		nextrow: 0,

		lineHeight: lineHeight,
		colwidths:  []float64{},
		rowheights: []float64{},
		hasWrited:  2 ^ 32,
	}

	for i := 0; i < cols; i++ {
		t.colwidths = append(t.colwidths, float64(1.0)/float64(cols))
	}

	cells := make([][]*TableCell, rows)
	for i := range cells {
		cells[i] = make([]*TableCell, cols)
	}

	t.cells = cells

	return t
}

// 创建长宽为1的单元格
func (table *Table) NewCell() *TableCell {
	row, col := table.nextrow, table.nextcol
	if row == -1 && col == -1 {
		panic("there has no cell")
	}

	cell := &TableCell{
		row:       row,
		col:       col,
		rowspan:   1,
		colspan:   1,
		table:     table,
		height:    table.lineHeight,
		minheight: table.lineHeight,
	}

	table.cells[row][col] = cell

	// 计算nextcol, nextrow
	table.setNext(1, 1)

	return cell
}

// 创建固定长度的单元格
func (table *Table) NewCellByRange(w, h int) *TableCell {
	colspan, rowspan := w, h
	if colspan <= 0 || rowspan <= 0 {
		panic("w and h must more than 0")
	}

	if colspan == 1 && rowspan == 1 {
		return table.NewCell()
	}

	row, col := table.nextrow, table.nextcol
	if row == -1 && col == -1 {
		panic("there has no cell")
	}

	// 防止非法的宽度
	if !table.checkSpan(row, col, rowspan, colspan) {
		panic("inlivid layout, please check w and h")
	}

	cell := &TableCell{
		row:       row,
		col:       col,
		rowspan:   rowspan,
		colspan:   colspan,
		table:     table,
		height:    table.lineHeight * float64(h),
		minheight: table.lineHeight,
	}

	table.cells[row][col] = cell

	// 构建空白单元格
	for i := 0; i < rowspan; i++ {
		var j int
		if i == 0 {
			j = 1
		}

		for ; j < colspan; j++ {
			table.cells[row+i][col+j] = &TableCell{
				row:       row + i,
				col:       col + j,
				rowspan:   -row,
				colspan:   -col,
				table:     table,
				height:    table.lineHeight,
				minheight: table.lineHeight,
			}
		}
	}

	// 计算nextcol, nextrow, 需要遍历处理
	table.setNext(colspan, rowspan)

	return cell
}

// 检测当前cell的宽和高是否合法
func (table *Table) checkSpan(row, col int, rowspan, colspan int) bool {
	var (
		cells          = table.cells
		maxrow, maxcol int
	)

	// 获取单方面的最大maxrow和maxcol
	for i := col; i < table.cols; i++ {
		if cells[row][i] != nil {
			maxcol = table.cols - col + 1
		}

		if i == table.cols-1 {
			maxcol = table.cols
		}
	}

	for i := row; i < table.rows; i++ {
		if cells[i][col] != nil {
			maxrow = table.rows - row + 1
		}

		if i == table.rows-1 {
			maxrow = table.rows
		}
	}

	if rowspan == 1 && colspan <= maxcol || colspan == 1 && rowspan <= maxrow {
		return true
	}

	// 检测合法性
	if colspan <= maxcol && rowspan <= maxrow {
		for i := row; i < row+rowspan; i++ {
			for j := col; j < col+colspan; j++ {
				if cells[i][j] != nil {
					return false
				}
			}
		}

		return true
	}

	return false
}

// 设置下一个单元格开始坐标
func (table *Table) setNext(colspan, rowspan int) {
	table.nextcol += colspan
	if table.nextcol == table.cols {
		table.nextcol = 0
		table.nextrow += 1
	}

	// 获取最近行的空白Cell的坐标
	for i := table.nextrow; i < table.rows; i++ {
		var j int
		if i == table.nextrow {
			j = table.nextcol
		}

		for ; j < table.cols; j++ {
			if table.cells[i][j] == nil {
				table.nextrow, table.nextcol = i, j
				return
			}
		}
	}

	if table.nextrow == table.rows {
		table.nextcol = -1
		table.nextrow = -1
	}
}

/********************************************************************************************************************/

// 获取某列的宽度
func (table *Table) GetColWidth(row, col int) float64 {
	if row < 0 || row > len(table.cells) || col < 0 || col > len(table.cells[row]) {
		panic("the index out range")
	}

	count := 0.0
	for i := 0; i < table.cells[row][col].colspan; i++ {
		count += table.colwidths[i+col] * table.width
	}

	return count
}

// 设置表的行高, 行高必须大于当前使用字体的行高
func (table *Table) SetLineHeight(lineHeight float64) {
	table.lineHeight = lineHeight
}

// 设置表的外
func (table *Table) SetMargin(margin core.Scope) {
	margin.ReplaceMarign()
	table.margin = margin
}

/********************************************************************************************************************/

func (table *Table) GenerateAtomicCell() error {
	var (
		sx, sy        = table.pdf.GetXY() // 基准坐标
		pageEndY      = table.pdf.GetPageEndY()
		x1, y1, _, y2 float64 // 当前位置
	)

	// 重新计算行高, 并且缓存每个位置的开始坐标
	table.resetCellHeight()
	table.cachedPoints(sx, sy)

	for i := 0; i < table.rows; i++ {
		for j := 0; j < table.cols; j++ {
			// cell的rowspan是1
			_, y1, _, y2 = table.getVLinePosition(sx, sy, j, i)
			if table.cells[i][j].rowspan > 1 {
				y2 = y1 + table.cells[i][j].minheight
			}

			// 换页
			if y1 < pageEndY && y2 > pageEndY {
				if i == 0 {
					table.pdf.AddNewPage(false)
					table.hasWrited = 2 ^ 32
					table.margin.Top = 0
					table.pdf.SetXY(table.pdf.GetPageStartXY())
					return table.GenerateAtomicCell()
				}

				// 写完剩余的内容
				table.writeCurrentPageRestCells(i, j, sx, sy)

				// 调整hasWrited的值
				if table.hasWrited > table.cells[i][j].row-table.cells[0][0].row {
					table.hasWrited = table.cells[i][j].row - table.cells[0][0].row
				}

				// 画当前页面边框线
				table.drawPageLines(sx, sy)

				// 重置tableCells
				table.resetTableCells()

				// 相关动态变量重置
				table.pdf.AddNewPage(false)
				table.margin.Top = 0
				table.rows = len(table.cells)
				table.hasWrited = 2 ^ 32
				table.pdf.SetXY(table.pdf.GetPageStartXY())

				table.pdf.LineType("straight", 0.1)
				table.pdf.GrayStroke(0)

				if table.rows == 0 {
					return nil
				}

				return table.GenerateAtomicCell()
			}

			if table.cells[i][j].element == nil {
				continue
			}

			x1, y1, _, y2 = table.getVLinePosition(sx, sy, j, i) // 真实的垂直线

			// 当前cell高度跨页
			if y1 < pageEndY && y2 > pageEndY {
				if table.hasWrited > table.cells[i][j].row-table.cells[0][0].row {
					table.hasWrited = table.cells[i][j].row - table.cells[0][0].row
				}
				table.writePartialPageCell(i, j, sx, sy) // 部分写入
			}

			// 当前celll没有跨页
			if y1 < pageEndY && y2 < pageEndY {
				table.writeCurrentPageCell(i, j, sx, sy)
			}
		}
	}

	// 最后一个页面的最后部分
	table.drawLastPageLines(sx, sy)

	// 重置当前的坐标(非常重要)
	height := table.getLastPageHeight()
	_, y1, _, y2 = table.getVLinePosition(sx, sy, 0, 0)
	x1, _ = table.pdf.GetPageStartXY()
	table.pdf.SetXY(x1, y1+height+table.margin.Top+table.margin.Bottom)

	return nil
}

// row,col 定位cell, sx,sy是table基准坐标
func (table *Table) writeCurrentPageCell(row, col int, sx, sy float64) {
	var (
		x1, y1, _, y2 float64
		pageEndY      = table.pdf.GetPageEndY()
		cell          = table.cells[row][col]
	)

	x1, y1, _, y2 = table.getVLinePosition(sx, sy, col, row)
	cell.table.pdf.SetXY(x1, y1)

	if cell.element != nil {
		// 检查当前Cell下面的Cell能否写入(下一个Cell跨页), 如果不能写入, 需要修正写入的高度值
		i, j := cell.row+cell.rowspan-table.cells[0][0].row, cell.col-table.cells[0][0].col
		if i < len(table.cells) {
			_, y3, _, y4 := table.getVLinePosition(sx, sy, j, i)
			if y3 < pageEndY && y4 >= pageEndY {
				if !table.checkNextCellCanWrite(sx, sy, row, col) {
					y2 = pageEndY
				}
			}
		}

		cell.element.GenerateAtomicCell(y2 - y1)
		cell.cellwrited = cell.rowspan
	}
}
func (table *Table) writePartialPageCell(row, col int, sx, sy float64) {
	var (
		x1, y1   float64
		pageEndY = table.pdf.GetPageEndY()
		cell     = table.cells[row][col]
	)

	x1, y1, _, _ = table.getVLinePosition(sx, sy, col, row) // 垂直线
	cell.table.pdf.SetXY(x1, y1)

	if cell.element != nil {
		// 尝试写入(跨页的Cell), 写不进去就不再写
		wn, _ := cell.element.TryGenerateAtomicCell(pageEndY - y1)
		if wn == 0 {
			return
		}

		// 真正的写入
		n, _, _ := cell.element.GenerateAtomicCell(pageEndY - y1)

		// 统计写入的行数
		if n > 0 && cell.element.GetHeight() == 0 {
			cell.cellwrited = cell.rowspan
		}

		if n > 0 && cell.rowspan > 1 && cell.element.GetHeight() != 0 {
			count := 0
			for i := row; i < row+cell.rowspan; i++ {
				_, y1, _, y2 := table.getVLinePosition(sx, sy, col, i)
				if table.cells[i][col].element != nil {
					y2 = y1 + table.cells[i][col].minheight
				}

				if y1 < pageEndY && y2 <= pageEndY {
					count++
				}
				if y1 > pageEndY || y2 > pageEndY {
					break
				}
			}

			cell.cellwrited = count
		}
	}
}

// 当前页面的剩余内容
func (table *Table) writeCurrentPageRestCells(row, col int, sx, sy float64) {
	var (
		x1, y1   float64
		pageEndY = table.pdf.GetPageEndY()
	)

	for i := col; i < table.cols; i++ {
		cell := table.cells[row][i]

		if cell.element == nil {
			continue
		}

		// 坐标变换
		x1, y1, _, _ = table.getHLinePosition(sx, sy, i, row) // 计算初始点
		cell.table.pdf.SetXY(x1, y1)

		// 下一页的Cell
		if y1 > pageEndY {
			continue
		}

		// 尝试写入(跨页的Cell), 写不进去就不再写
		wn, _ := cell.element.TryGenerateAtomicCell(pageEndY - y1)
		if wn == 0 {
			continue
		}

		// 真正的写入
		n, _, _ := cell.element.GenerateAtomicCell(pageEndY - y1)

		// 统计写入的行数
		if n > 0 && cell.element.GetHeight() == 0 {
			cell.cellwrited = cell.rowspan
		}

		if n > 0 && cell.rowspan > 1 && cell.element.GetHeight() != 0 {
			count := 0
			for k := row; k < row+cell.rowspan; k++ {
				_, y1, _, y2 := table.getVLinePosition(sx, sy, i, k)
				if table.cells[k][i].element != nil {
					y2 = y1 + table.cells[k][i].minheight
				}

				if y1 < pageEndY && y2 <= pageEndY {
					count++
				}
				if y1 > pageEndY || y2 > pageEndY {
					break
				}
			}

			cell.cellwrited = count
		}
	}
}

// 检查下一个Cell是否可以写入(当前的Cell必须是非空格Cell)
func (table *Table) checkNextCellCanWrite(sx, sy float64, row, col int) bool {
	var (
		canwrite bool
		cells    = table.cells
		pageEndY = table.pdf.GetPageEndY()
	)

	if cells[row][col].rowspan <= 0 {
		return canwrite
	}

	// 当前cell的下一行
	nextrow := cells[row][col].row + cells[row][col].rowspan - cells[0][0].row
	for k := col; k < table.cols; k++ {
		cell := cells[nextrow][col]
		_, y, _, _ := table.getHLinePosition(sx, sy, col, nextrow)

		// 空格Cell -> 寻找非空格Cell
		if cell.rowspan <= 0 {
			i, j := -cell.rowspan-cells[0][0].row, -cell.colspan-cells[0][0].col
			wn, _ := cells[i][j].element.TryGenerateAtomicCell(pageEndY - y)
			if wn > 0 {
				canwrite = true
				return canwrite
			}
		}

		// 非空格Cell
		if cell.rowspan >= 1 {
			wn, _ := cell.element.TryGenerateAtomicCell(pageEndY - y)
			if wn > 0 {
				canwrite = true
				return canwrite
			}
		}
	}

	return canwrite
}

// 对当前的Page进行画线
func (table *Table) drawPageLines(sx, sy float64) {
	var (
		rows, cols           = table.rows, table.cols
		pageEndY             = table.pdf.GetPageEndY()
		x, y, x1, y1, x2, y2 float64
	)

	// todo: 只计算当前页面最大的rows
	x1, _ = table.pdf.GetPageStartXY()
	x2 = table.pdf.GetPageEndY()
	if rows > int((x2-x1)/table.lineHeight)+1 {
		rows = int((x2-x1)/table.lineHeight) + 1
	}

	table.pdf.LineType("straight", 0.1)
	table.pdf.GrayStroke(0)

	// 两条水平线
	x, y, _, _ = table.getHLinePosition(sx, sy, 0, 0)
	table.pdf.LineH(x, y, x+table.width)
	table.pdf.LineH(x, pageEndY, x+table.width)

	for row := 0; row < rows; row++ {
		for col := 0; col < cols; col++ {
			cell := table.cells[row][col]

			if cell.element == nil {
				continue
			}

			// 坐标化
			x, y, x1, y1 = table.getHLinePosition(sx, sy, col, row)
			x, y, _, y2 = table.getVLinePosition(sx, sy, col, row)

			// TODO: 当前的Cell没有跨页
			if y1 < pageEndY && y2 < pageEndY {
				// todo: 当前Cell的下一个Cell跨页, 需要判断下一个Cell是否可以写入
				i, j := cell.row+cell.rowspan-table.cells[0][0].row, cell.col-table.cells[0][0].col
				_, y3, _, y4 := table.getVLinePosition(sx, sy, j, i)
				if y3 < pageEndY && y4 >= pageEndY {
					if !table.checkNextCellWrite(row, col) {
						y2 = pageEndY
						table.pdf.LineV(x1, y1, y2)
						table.pdf.LineH(x, y2, x1)
						continue
					}
				}

				table.pdf.LineV(x1, y1, y2)
				table.pdf.LineH(x, y2, x1)
			}

			// TODO: 当前的Cell跨页, 需要先判断是否需要竖线
			if y1 < pageEndY && y2 >= pageEndY {
				if table.checkNeedVline(row, col) {
					table.pdf.LineV(x1, y1, pageEndY)
				}

				table.pdf.LineH(x, pageEndY, x1)
			}
		}
	}

	// 两条垂直线
	x, y, _, _ = table.getHLinePosition(sx, sy, 0, 0)
	table.pdf.LineV(x, y, pageEndY)
	table.pdf.LineV(x+table.width, y, pageEndY)
}

// 最后一页画线(基本参考了drawPageLines)
func (table *Table) drawLastPageLines(sx, sy float64) {
	var (
		rows, cols          = table.rows, table.cols
		pageEndY            = table.getLastPageHeight()
		x, y, x1, y1, _, y2 float64
	)

	table.pdf.LineType("straight", 0.1)
	table.pdf.GrayStroke(0)

	x, y, _, _ = table.getHLinePosition(sx, sy, 0, 0)
	pageEndY = y + pageEndY

	table.pdf.LineH(x, y, x+table.width)
	table.pdf.LineH(x, pageEndY, x+table.width)

	for row := 0; row < rows; row++ {
		for col := 0; col < cols; col++ {
			cell := table.cells[row][col]

			if cell.element == nil {
				continue
			}

			x, y, x1, y1 = table.getHLinePosition(sx, sy, col, row)
			x, y, _, y2 = table.getVLinePosition(sx, sy, col, row)

			if y1 < pageEndY && y2 < pageEndY {
				table.pdf.LineV(x1, y1, y2)
				table.pdf.LineH(x, y2, x1)
			}

			if y1 < pageEndY && y2 >= pageEndY {
				table.pdf.LineV(x1, y1, pageEndY)
				table.pdf.LineH(x, pageEndY, x1)
			}
		}
	}

	x, y, _, _ = table.getHLinePosition(sx, sy, 0, 0)
	table.pdf.LineV(x, y, pageEndY)
	table.pdf.LineV(x+table.width, y, pageEndY)
}

func (table *Table) checkNextCellWrite(row, col int) bool {
	var (
		cells      = table.cells
		cellwrited bool
	)

	if cells[row][col].rowspan <= 0 {
		return cellwrited
	}

	nextrow := cells[row][col].row + cells[row][col].rowspan - cells[0][0].row
	for k := col; k < table.cols; k++ {
		cell := cells[nextrow][col]

		if cell.rowspan <= 0 {
			i, j := -cell.rowspan-cells[0][0].row, -cell.colspan-cells[0][0].col
			// todo: 当前空白Cell已经写入内容
			if cells[i][j].cellwrited >= cell.row-cells[i][j].row+1 {
				cellwrited = true
				return cellwrited
			}
		}

		if cell.rowspan == 1 {
			// todo: 当前的Cell存在写入的内容
			height := cell.element.GetHeight()
			lastheight := cell.element.GetLastHeight()
			if cell.element.GetHeight() == 0 || math.Abs(lastheight-height) > 0.1 {
				cellwrited = true
				return cellwrited
			}
		}

		if cell.rowspan > 1 {
			// todo: 当前的Cell存在写入的内容
			if cell.cellwrited > 0 {
				cellwrited = true
				return cellwrited
			}
		}
	}

	return cellwrited
}

// 跨页的Cell
func (table *Table) checkNeedVline(row, col int) bool {
	var (
		negwrited bool
		curwrited bool
		cells     = table.cells
		origin    = cells[0][0]
	)

	if cells[row][col].rowspan <= 0 {
		return negwrited || curwrited
	}

	// todo: 当前cell没有写入 && 邻居Cell没有写入 => 不需要线, 其余的都须要
	// 当前的cell
	if cells[row][col].rowspan == 1 {
		height := cells[row][col].element.GetHeight()
		lastheight := cells[row][col].element.GetLastHeight()
		if cells[row][col].element.GetHeight() == 0 || math.Abs(lastheight-height) > 0.1 {
			curwrited = true
		}
	}
	if cells[row][col].rowspan > 1 {
		if cells[row][col].cellwrited > 0 {
			curwrited = true
		}
	}

	// 邻居cell
	nextcol := cells[row][col].col + cells[row][col].colspan - cells[0][0].col
	if nextcol == table.cols {
		negwrited = true
		return negwrited || curwrited
	}

	if cells[row][nextcol].rowspan <= 0 {
		row, nextcol = -cells[row][nextcol].rowspan-origin.row, -cells[row][nextcol].colspan-origin.col
	}

	if cells[row][nextcol].rowspan == 1 {
		height := cells[row][nextcol].element.GetHeight()
		lastheight := cells[row][nextcol].element.GetLastHeight()
		if cells[row][nextcol].element.GetHeight() == 0 || math.Abs(lastheight-height) > 0.1 {
			negwrited = true
			return negwrited || curwrited
		}
	}

	if cells[row][nextcol].rowspan > 1 {
		if cells[row][nextcol].cellwrited > 0 {
			negwrited = true
			return negwrited || curwrited
		}
	}

	return negwrited || curwrited
}

// 校验table是否合法(只做一次)
func (table *Table) checkTableConstraint() {
	if !table.tableCheck {
		return
	}

	table.tableCheck = false
	var (
		cells int
		area  int
	)
	for i := 0; i < table.rows; i++ {
		for j := 0; j < table.cols; j++ {
			cell := table.cells[i][j]
			if cell != nil {
				cells += 1
			}
			if cell != nil && cell.element != nil {
				area += cell.rowspan * cell.colspan
			}
		}
	}

	if cells != table.cols*table.rows || area != table.cols*table.rows {
		panic("please check setting rows, cols and writed cell")
	}
}

// TODO: 重新计算tablecell的高度(精确)
func (table *Table) resetCellHeight() {
	table.checkTableConstraint()

	// todo: 只计算当前页面最大的rows
	x1, _ := table.pdf.GetPageStartXY()
	x2 := table.pdf.GetPageEndY()
	rows := table.rows
	if rows > int((x2-x1)/table.lineHeight)+1 {
		rows = int((x2-x1)/table.lineHeight) + 1
	}
	cells := table.cells

	// 对于cells的元素重新赋值height和minheight
	for i := 0; i < rows; i++ {
		for j := 0; j < table.cols; j++ {
			if cells[i][j] != nil && cells[i][j].element == nil {
				cells[i][j].minheight = table.lineHeight
				cells[i][j].height = table.lineHeight
			}

			if cells[i][j] != nil && cells[i][j].element != nil {
				cells[i][j].height = cells[i][j].element.GetHeight()
				if cells[i][j].rowspan == 1 {
					cells[i][j].minheight = cells[i][j].height
				}
			}
		}
	}

	// 第一遍计算rowspan是1的高度
	for i := 0; i < rows; i++ {
		var max float64 // 当前行的最大高度
		for j := 0; j < table.cols; j++ {
			if cells[i][j] != nil && max < cells[i][j].minheight {
				max = cells[i][j].minheight
			}
		}

		for j := 0; j < table.cols; j++ {
			if cells[i][j] != nil {
				cells[i][j].minheight = max // todo: 当前行(包括空白)的自身高度

				if cells[i][j].rowspan == 1 || cells[i][j].rowspan < 0 {
					cells[i][j].height = max
				}
			}
		}
	}

	// 第二遍计算rowsapn非1的行高度
	for i := 0; i < rows; i++ {
		for j := 0; j < table.cols; j++ {
			if cells[i][j] != nil && cells[i][j].rowspan > 1 {

				var totalHeight float64
				for k := 0; k < cells[i][j].rowspan; k++ {
					totalHeight += cells[i+k][j].minheight // todo: 计算所有行的高度
				}

				if totalHeight < cells[i][j].height {
					h := cells[i][j].height - totalHeight

					row := (cells[i][j].row - cells[0][0].row) + cells[i][j].rowspan - 1 // 最后一行
					for col := 0; col < table.cols; col++ {
						// 更新minheight
						cells[row][col].minheight += h

						// todo: 更新height, 当rowspan=1
						if cells[row][col].rowspan == 1 {
							cells[row][col].height += h
						}

						// todo: 更新height, 当rowspan<0, 空格,需要更新非当前的实体(前面的)
						if cells[row][col].rowspan <= 0 {
							cells[row][col].height += h

							orow := -cells[row][col].rowspan - cells[0][0].row
							ocol := -cells[row][col].colspan - cells[0][0].col
							if orow == i && ocol < j || orow < i {
								cells[orow][ocol].height += h
							}
						}
					}
				} else {
					cells[i][j].height = totalHeight
				}
			}
		}
	}

	table.cells = cells
}

func (table *Table) resetTableCells() {
	var (
		min    = 2 ^ 32
		cells  = table.cells
		origin = cells[0][0]
	)

	// 获取写入的最小行数
	for col := 0; col < table.cols; col++ {
		count := 0
		cell := cells[table.hasWrited][col]

		if cell.rowspan <= 0 {
			i, j := -cell.rowspan-origin.row, -cell.colspan-origin.col

			// todo: 从cells[table.hasWrite]行开始算起已经写入的行数
			count += cells[i][j].cellwrited - (cell.row - cells[i][j].row)

			if cells[i][j].cellwrited == cells[i][j].rowspan {
				// TODO: 先计算当前的实体(如果是空格, 找到空格对应的实体), 然后跳跃到下一个实体(条件性)
				srow := cells[i][j].row - origin.row + cells[i][j].rowspan
				count += table.countStandardRow(srow, col)
			}
		}

		if cell.rowspan >= 1 {
			count += cell.cellwrited

			if cell.cellwrited == cell.rowspan {
				// TODO: 先计算当前的实体, 然后跳跃到下一个实体(条件性)
				srow := cell.row - origin.row + cell.rowspan
				count += table.countStandardRow(srow, col)
			}
		}

		if min > count {
			min = count
		}
	}

	// cell重置,需要修正空格Cell
	row := table.hasWrited + min
	if row >= len(table.cells) { // TODO: 这里的判断是修复未知的错误
		row = len(table.cells) - 1
	}

	for col := 0; col < table.cols; {
		cell := table.cells[row][col]

		if cell.rowspan <= 0 {
			i, j := -cell.rowspan-origin.row, -cell.colspan-origin.col
			var ox, oy int

			for x := row; x < cells[i][j].row+cells[i][j].rowspan-origin.row; x++ {
				for y := col; y < col+cells[i][j].colspan; y++ {
					if x == row && y == col {
						ox, oy = cells[x][y].row, cells[x][y].col

						cells[x][y].element = cells[i][j].element
						cells[x][y].rowspan = cells[i][j].rowspan - (ox - cells[i][j].row)
						cells[x][y].colspan = cells[i][j].colspan
						cells[x][y].cellwrited = 0

						continue
					}

					cells[x][y].rowspan = -ox
					cells[x][y].colspan = -oy
				}
			}

			col += cells[i][j].colspan
			continue
		}

		if cell.rowspan >= 1 {
			col += cell.colspan
			cell.cellwrited = 0
			continue
		}
	}

	table.cells = table.cells[table.hasWrited+min:]
}

func (table *Table) countStandardRow(srow, scol int) int {
	var (
		origin = table.cells[0][0]
		count  int
	)
	for row := srow; row < table.rows; {
		cell := table.cells[row][scol]

		if cell.rowspan <= 0 {
			i, j := -cell.rowspan-origin.row, -cell.colspan-origin.col // 实体
			if table.cells[i][j].cellwrited == 0 { // 当前的实体未写
				break
			}

			if table.cells[i][j].cellwrited >= cell.row-table.cells[i][j].row+1 {
				count += table.cells[i][j].cellwrited - (cell.row - table.cells[i][j].row)
			}

			row = table.cells[i][j].row - origin.row + table.cells[i][j].rowspan // 实体的下一个"实体"
		}

		if cell.rowspan >= 1 {
			if cell.cellwrited == 0 { // 当前的实体未写
				break
			}

			count += cell.cellwrited

			if cell.rowspan > cell.cellwrited { // 当前的实体未写完
				break
			}

			row += cell.rowspan
		}
	}

	return count
}

func (table *Table) cachedPoints(sx, sy float64) {
	// 只计算当前页面最大的rows
	x1, _ := table.pdf.GetPageStartXY()
	x2 := table.pdf.GetPageEndY()
	rows := table.rows
	if rows > int((x2-x1)/table.lineHeight)+1 {
		rows = int((x2-x1)/table.lineHeight) + 1
	}

	var (
		x, y = sx+table.margin.Left, sy+table.margin.Top
	)

	// 只会缓存一次
	if table.cachedCol == nil {
		table.cachedCol = make([]float64, table.cols)

		for col := 0; col < table.cols; col++ {
			table.cachedCol[col] = x
			x += table.colwidths[col] * table.width
		}
	}

	table.cachedRow = make([]float64, rows)

	for row := 0; row < rows; row++ {
		table.cachedRow[row] = y
		y += table.cells[row][0].minheight
	}
}

// 垂直线, table单元格的垂直线
func (table *Table) getVLinePosition(sx, sy float64, col, row int) (x1, y1 float64, x2, y2 float64) {
	var (
		x, y float64
		cell = table.cells[row][col]
	)

	x = table.cachedCol[col]
	y = table.cachedRow[row]

	return x, y, x, y + cell.height
}

// 水平线, table单元格的水平线
func (table *Table) getHLinePosition(sx, sy float64, col, row int) (x1, y1 float64, x2, y2 float64) {
	var (
		x, y float64
	)

	x = table.cachedCol[col]
	y = table.cachedRow[row]

	cell := table.cells[row][col]
	if cell.colspan > 1 {
		if cell.col+cell.colspan == table.cols {
			x1 = table.cachedCol[0] + table.width
		} else {
			x1 = table.cachedCol[cell.col+cell.colspan]
		}
	} else {
		x1 = x + table.colwidths[col]*table.width
	}

	return x, y, x1, y
}

// 获取表的垂直高度
func (table *Table) getLastPageHeight() float64 {
	var count float64
	for i := 0; i < table.rows; i++ {
		count += table.cells[i][0].minheight
	}
	return count
}
