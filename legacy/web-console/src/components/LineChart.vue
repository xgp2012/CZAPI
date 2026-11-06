<script setup lang="ts">
/**
 * ECharts wrapper.
 *
 * A single chart type (line/area) covers every trend in this product, so the
 * wrapper owns the axis styling and only accepts series data. It re-renders on
 * theme change because ECharts colours cannot come from CSS variables.
 */
import { onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import * as echarts from 'echarts'
import { chartPalette, theme } from '@/utils/theme'

export interface LineSeries {
  name: string
  data: number[]
  /** 'accent' (spend/usage) or 'steel' (secondary comparison). */
  tone?: 'accent' | 'steel'
  /** Render as bars on the secondary axis. */
  bar?: boolean
  yAxisIndex?: number
  axis?: 'left' | 'right'
}

const props = withDefaults(
  defineProps<{
    categories: string[]
    series: LineSeries[]
    height?: number
    yName?: string
    yNameRight?: string
    tooltipFormatter?: (params: unknown) => string
    area?: boolean
    smooth?: boolean
  }>(),
  { height: 260, area: true, smooth: true },
)

const el = ref<HTMLDivElement | null>(null)
const chart = shallowRef<echarts.ECharts | null>(null)
let observer: ResizeObserver | null = null

function buildOption(): echarts.EChartsCoreOption {
  const p = chartPalette()
  const hasRight = props.series.some((s) => s.axis === 'right')

  const series = props.series.map((s) => {
    const color = s.tone === 'steel' ? p.secondary : p.accent
    const fill = s.tone === 'steel' ? p.secondaryFill : p.accentFill
    if (s.bar) {
      return {
        name: s.name,
        type: 'bar' as const,
        data: s.data,
        yAxisIndex: s.axis === 'right' ? 1 : 0,
        barMaxWidth: 14,
        itemStyle: { color, borderRadius: [2, 2, 0, 0], opacity: 0.85 },
        emphasis: { itemStyle: { opacity: 1 } },
        z: 1,
      }
    }
    return {
      name: s.name,
      type: 'line' as const,
      data: s.data,
      yAxisIndex: s.axis === 'right' ? 1 : 0,
      smooth: props.smooth ? 0.28 : false,
      symbol: 'circle',
      symbolSize: 4,
      showSymbol: false,
      lineStyle: { width: 1.8, color },
      itemStyle: { color },
      areaStyle: props.area ? { color: fill } : undefined,
      emphasis: { focus: 'series' as const },
      z: 2,
    }
  })

  return {
    animationDuration: 520,
    animationEasing: 'cubicOut',
    grid: { left: 8, right: hasRight ? 12 : 8, top: 26, bottom: 4, containLabel: true },
    legend: {
      show: props.series.length > 1,
      right: 0,
      top: 0,
      icon: 'roundRect',
      itemWidth: 8,
      itemHeight: 8,
      itemGap: 14,
      textStyle: { color: p.axis, fontSize: 11 },
    },
    tooltip: {
      trigger: 'axis',
      backgroundColor: p.tooltipBg,
      borderColor: p.tooltipBorder,
      borderWidth: 1,
      padding: [8, 12],
      textStyle: { color: p.text, fontSize: 12 },
      axisPointer: { type: 'line', lineStyle: { color: p.split, width: 1 } },
      formatter: props.tooltipFormatter as never,
    },
    xAxis: {
      type: 'category',
      data: props.categories,
      boundaryGap: props.series.some((s) => s.bar),
      axisLine: { lineStyle: { color: p.split } },
      axisTick: { show: false },
      axisLabel: { color: p.axis, fontSize: 11, hideOverlap: true },
    },
    yAxis: [
      {
        type: 'value',
        name: props.yName,
        nameTextStyle: { color: p.axis, fontSize: 11, align: 'left' },
        splitLine: { lineStyle: { color: p.split, type: 'dashed' } },
        axisLabel: { color: p.axis, fontSize: 11 },
        axisLine: { show: false },
      },
      ...(hasRight
        ? [
            {
              type: 'value' as const,
              name: props.yNameRight,
              nameTextStyle: { color: p.axis, fontSize: 11, align: 'right' },
              splitLine: { show: false },
              axisLabel: { color: p.axis, fontSize: 11 },
              axisLine: { show: false },
            },
          ]
        : []),
    ],
    series: series as never,
  }
}

function render() {
  if (!chart.value) return
  chart.value.setOption(buildOption(), true)
}

onMounted(() => {
  if (!el.value) return
  chart.value = echarts.init(el.value, undefined, { renderer: 'canvas' })
  render()
  observer = new ResizeObserver(() => chart.value?.resize())
  observer.observe(el.value)
})

watch(() => [props.categories, props.series], render, { deep: true })
watch(theme, () => render())

onBeforeUnmount(() => {
  observer?.disconnect()
  chart.value?.dispose()
  chart.value = null
})
</script>

<template>
  <div ref="el" class="chart" :style="{ height: `${height}px` }" />
</template>

<style scoped>
.chart {
  width: 100%;
}
</style>
