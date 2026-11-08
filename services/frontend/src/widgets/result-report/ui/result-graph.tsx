import { Heading, Popover, Text } from '@radix-ui/themes'
import { type CSSProperties, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { layoutResultGraph } from '../lib/result-graph'
import { type ReportMark, type ReportTopic, statusOrder } from '../model/report'
import styles from './result-report.module.scss'

// Card and spacing sizes of the map, in pixels.
const NODE_WIDTH = 208
const NODE_HEIGHT = 92
const COLUMN_GAP = 64
const ROW_GAP = 20
const PADDING = 28

// The checked topics and tasks: which relies on which, with the points of
// every one. Prerequisites stand left of the topics on them. A card with marks
// or a remark opens them.
export function ResultGraph({ topics }: { topics: ReportTopic[] }) {
  const { t } = useTranslation()
  const graph = useMemo(() => layoutResultGraph(topics), [topics])
  // The details open inside the screen, where its palette is defined.
  const [panel, setPanel] = useState<HTMLElement | null>(null)
  if (graph.nodes.length === 0) return null

  const x = (column: number) => PADDING + column * (NODE_WIDTH + COLUMN_GAP)
  const y = (row: number) => PADDING + row * (NODE_HEIGHT + ROW_GAP)
  const width = x(graph.columns) - COLUMN_GAP + PADDING
  const height = y(graph.rows) - ROW_GAP + PADDING
  const place = new Map(graph.nodes.map((node) => [node.topic.id, node]))
  const statuses = statusOrder.filter((status) =>
    graph.nodes.some((node) => node.topic.status === status),
  )

  return (
    <section
      ref={setPanel}
      aria-labelledby="graph-title"
      className={styles.panel}
    >
      <Heading as="h2" size="5" id="graph-title">
        {t('resultReport.graphTitle')}
      </Heading>
      <Text as="p" className={styles.lead}>
        {t('resultReport.graphHint')}
      </Text>
      <ul className={styles.legend} aria-label={t('resultReport.graphLegend')}>
        {statuses.map((status) => (
          <li key={status} data-status={status}>
            {t(`resultReport.status.${status}`)}
          </li>
        ))}
      </ul>
      {/* A wide map scrolls sideways; the region is focusable for keyboards. */}
      <div
        className={styles.graphScroll}
        role="region"
        aria-label={t('resultReport.graphScroll')}
        tabIndex={0}
      >
        <div
          className={styles.graphCanvas}
          style={{ width, height } as CSSProperties}
        >
          <svg
            className={styles.graphEdges}
            width={width}
            height={height}
            aria-hidden="true"
          >
            <defs>
              <marker
                id="graph-arrow"
                viewBox="0 0 10 10"
                refX="9"
                refY="5"
                markerWidth="7"
                markerHeight="7"
                orient="auto-start-reverse"
              >
                <path d="M0,0 L10,5 L0,10 z" className={styles.graphArrow} />
              </marker>
              {graph.edges.map((edge, index) => {
                const from = place.get(edge.from)!
                const to = place.get(edge.to)!
                return (
                  <linearGradient
                    key={index}
                    id={`graph-edge-${index}`}
                    gradientUnits="userSpaceOnUse"
                    x1={x(from.column) + NODE_WIDTH}
                    x2={x(to.column)}
                    y1={0}
                    y2={0}
                  >
                    <stop offset="0%" className={styles.edgeStart} />
                    <stop offset="50%" className={styles.edgeMiddle} />
                    <stop offset="100%" className={styles.edgeEnd} />
                  </linearGradient>
                )
              })}
            </defs>
            {graph.edges.map((edge, index) => {
              const from = place.get(edge.from)!
              const to = place.get(edge.to)!
              // The edge leaves the right side of the prerequisite, runs
              // straight through its lane slots and enters the topic's left side.
              const middle = (row: number) => y(row) + NODE_HEIGHT / 2
              const points = [
                [x(from.column) + NODE_WIDTH, middle(from.row)],
                ...edge.via.flatMap(({ column, row }) => [
                  [x(column), middle(row)],
                  [x(column) + NODE_WIDTH, middle(row)],
                ]),
                [x(to.column) - 4, middle(to.row)],
              ]
              let d = `M${points[0][0]},${points[0][1]}`
              for (let i = 1; i < points.length; i++) {
                const [ax, ay] = points[i - 1]
                const [bx, by] = points[i]
                // Odd steps cross a column gap with a curve; even ones run along a lane.
                if (i % 2 === 1) {
                  const bend = (bx - ax) / 2
                  d += ` C${ax + bend},${ay} ${bx - bend},${by} ${bx},${by}`
                } else d += ` L${bx},${by}`
              }
              return (
                <path
                  key={index}
                  d={d}
                  className={styles.graphEdge}
                  data-gap={from.topic.status === 'gap' || undefined}
                  stroke={`url(#graph-edge-${index})`}
                  markerEnd="url(#graph-arrow)"
                />
              )
            })}
          </svg>
          <ol
            className={styles.graphNodes}
            aria-label={t('resultReport.graphNodes')}
          >
            {graph.nodes.map(({ topic, column, row }) => {
              const requires = graph.edges
                .filter((edge) => edge.to === topic.id)
                .map((edge) => place.get(edge.from)!.topic.title)
              return (
                <li
                  key={topic.id}
                  className={styles.graphNode}
                  data-status={topic.status}
                  data-top={topic.main || undefined}
                  style={
                    {
                      left: x(column),
                      top: y(row),
                      width: NODE_WIDTH,
                      height: NODE_HEIGHT,
                    } as CSSProperties
                  }
                >
                  <GraphNodeCard topic={topic} container={panel} />
                  {requires.length > 0 && (
                    <span className={styles.visuallyHidden}>
                      {t('resultReport.graphRequires', {
                        titles: requires.join(', '),
                      })}
                    </span>
                  )}
                </li>
              )
            })}
          </ol>
        </div>
      </div>
    </section>
  )
}

function GraphNodeCard({
  topic,
  container,
}: {
  topic: ReportTopic
  container: HTMLElement | null
}) {
  const { t } = useTranslation()
  const points = topic.score !== null && (
    <span className={styles.graphGrade}>
      {t('resultReport.points', { score: topic.score, max: topic.maxScore })}
    </span>
  )
  const card = (
    <>
      <span className={styles.graphNodeTitle} title={topic.title}>
        {topic.title}
      </span>
      <span className={styles.graphNodeMeta}>
        <span
          className={styles.status}
          title={t(`resultReport.status.${topic.status}`)}
        >
          {t(`resultReport.statusShort.${topic.status}`)}
        </span>
        {points}
      </span>
    </>
  )
  if (topic.marks.length === 0 && !topic.skipped && !topic.assisted) {
    return <div className={styles.graphNodeBody}>{card}</div>
  }
  return (
    <Popover.Root>
      <Popover.Trigger>
        <button type="button" className={styles.graphNodeBody}>
          {card}
        </button>
      </Popover.Trigger>
      <Popover.Content
        container={container}
        className={styles.graphDetails}
        data-status={topic.status}
        size="2"
        maxWidth="400px"
      >
        <Heading as="h3" size="3" className={styles.graphDetailsTitle}>
          {topic.title}
        </Heading>
        <div className={styles.graphDetailsMeta}>
          <span className={styles.status}>
            {t(`resultReport.status.${topic.status}`)}
          </span>
          {topic.skipped && (
            <Text className={styles.flag}>{t('resultReport.skipped')}</Text>
          )}
          {topic.assisted && (
            <Text className={styles.flag}>{t('resultReport.assisted')}</Text>
          )}
          {points}
        </div>
        {topic.marks.length > 0 && (
          <ul className={styles.marks}>
            {topic.marks.map((mark, index) => (
              <MarkRow key={mark.id} mark={mark} index={index} />
            ))}
          </ul>
        )}
      </Popover.Content>
    </Popover.Root>
  )
}

function MarkRow({ mark, index }: { mark: ReportMark; index: number }) {
  const { t } = useTranslation()
  // A ground of the score is shown as it is: it has no title or points.
  if (mark.score === undefined && mark.met === undefined)
    return (
      <li className={styles.mark}>
        <span className={styles.markBody}>
          <Text className={styles.markReason}>{mark.reason}</Text>
        </span>
      </li>
    )
  const title =
    mark.title || t('resultReport.markFallback', { number: index + 1 })
  return (
    <li className={styles.mark}>
      <span className={styles.markPoints}>
        {mark.score !== undefined
          ? t('resultReport.points', { score: mark.score, max: mark.maxScore })
          : t(mark.met ? 'resultReport.markMet' : 'resultReport.markNotMet')}
      </span>
      <span className={styles.markBody}>
        <Text className={styles.markTitle}>{title}</Text>
        {mark.reason && (
          <Text className={styles.markReason}>{mark.reason}</Text>
        )}
        {mark.quote && <Text className={styles.markQuote}>«{mark.quote}»</Text>}
      </span>
    </li>
  )
}
