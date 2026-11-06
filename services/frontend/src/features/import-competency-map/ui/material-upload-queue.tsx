import { Button, Text } from '@radix-ui/themes'
import {
  ChevronDown,
  ChevronUp,
  GripVertical,
  Send,
  Trash2,
  Upload,
} from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import styles from './material-upload-queue.module.scss'
import { MaterialUploadResultModal } from './material-upload-result-modal'
import {
  useMaterialUploadQueue,
  type MaterialQueueItem,
} from '../model/use-material-upload-queue'
import type { CourseMaterialKindOption } from '@/entities/competency-map'
import { CourseMaterialCard } from './course-material-card'
import { MaterialMetadataFields } from './material-metadata-fields'
import {
  moveMaterialQueueItem,
  moveMaterialQueueItemToIndex,
  sortMaterialsByWeek,
} from '../model/material-queue-order'

type MaterialUploadQueueState = ReturnType<typeof useMaterialUploadQueue>

export function MaterialReorderControls({
  name,
  disabled,
  isFirst,
  isLast,
  onMoveUp,
  onMoveDown,
}: {
  name: string
  disabled: boolean
  isFirst: boolean
  isLast: boolean
  onMoveUp: () => void
  onMoveDown: () => void
}) {
  const { t } = useTranslation()
  return (
    <div className={styles.reorderControls}>
      <Button
        type="button"
        variant="ghost"
        size="1"
        className={styles.orderButton}
        aria-label={t('competencyMap.queueMoveUp', { name })}
        disabled={disabled || isFirst}
        onClick={onMoveUp}
      >
        <ChevronUp aria-hidden="true" size={16} />
      </Button>
      <Button
        type="button"
        variant="ghost"
        size="1"
        className={styles.dragHandle}
        aria-label={t('competencyMap.queueDragItem', { name })}
        disabled={disabled}
        draggable={!disabled}
        data-queue-drag-handle="true"
      >
        <GripVertical aria-hidden="true" size={16} />
      </Button>
      <Button
        type="button"
        variant="ghost"
        size="1"
        className={styles.orderButton}
        aria-label={t('competencyMap.queueMoveDown', { name })}
        disabled={disabled || isLast}
        onClick={onMoveDown}
      >
        <ChevronDown aria-hidden="true" size={16} />
      </Button>
    </div>
  )
}

export function MaterialUploadQueueToolbar({
  queue,
  disabled = false,
}: {
  queue: MaterialUploadQueueState
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const unavailable = disabled || queue.isPending || queue.preparing
  return (
    <div className={styles.queue}>
      <label
        className={styles.add}
        aria-disabled={unavailable || undefined}
        data-disabled={unavailable || undefined}
        onDragOver={(event) => {
          if (!unavailable) return
          event.preventDefault()
          event.stopPropagation()
        }}
        onDrop={(event) => {
          if (!unavailable) return
          event.preventDefault()
          event.stopPropagation()
        }}
      >
        <span className={styles.addIcon} aria-hidden="true">
          <Upload size={22} />
        </span>
        <span className={styles.addText}>
          <span className={styles.addTitle}>{t('competencyMap.queueAdd')}</span>
          <span className={styles.addHint}>
            {t('competencyMap.queueAddHint')}
          </span>
        </span>
        <input
          className={styles.addInput}
          type="file"
          accept=".pdf,.zip,application/pdf,application/zip"
          disabled={unavailable}
          aria-label={t('competencyMap.queueAdd')}
          onChange={(event) => {
            if (!unavailable) void queue.addFile(event.currentTarget.files?.[0])
            event.currentTarget.value = ''
          }}
        />
      </label>
      <MaterialUploadResultModal
        notification={queue.notification}
        onClose={queue.dismissNotification}
      />
    </div>
  )
}

export function MaterialUploadQueue({
  userId,
  subjectId,
  kinds,
  disabled = false,
}: {
  userId: string
  subjectId: string
  kinds: CourseMaterialKindOption[]
  disabled?: boolean
}) {
  const queue = useMaterialUploadQueue(userId, subjectId, kinds)
  const [draggingId, setDraggingId] = useState<string | null>(null)
  const [dropTargetId, setDropTargetId] = useState<string | null>(null)
  const orderedItems = sortMaterialsByWeek(queue.items)

  function moveItem(id: string, targetIndex: number) {
    const current = orderedItems.map(({ id: itemId, week }) => ({
      id: itemId,
      week,
    }))
    queue.updateItems(moveMaterialQueueItemToIndex(current, id, targetIndex))
  }

  return (
    <>
      <MaterialUploadQueueToolbar queue={queue} disabled={disabled} />
      {orderedItems.length > 0 && (
        <div className={styles.items} role="list">
          {orderedItems.map((item, index) => (
            <div
              className={styles.item}
              key={item.id}
              role="listitem"
              draggable={!disabled && !queue.isPending && !queue.preparing}
              data-dragging={draggingId === item.id || undefined}
              onDragStart={(event) => {
                const target = event.target
                if (
                  target instanceof HTMLElement &&
                  target.closest('button, input, [role="combobox"]') &&
                  !target.closest('[data-queue-drag-handle]')
                ) {
                  event.preventDefault()
                  return
                }
                setDraggingId(item.id)
                event.dataTransfer.effectAllowed = 'move'
                event.dataTransfer.setData('text/plain', item.id)
              }}
              onDragEnd={() => {
                setDraggingId(null)
                setDropTargetId(null)
              }}
              onDragOver={(event) => {
                event.preventDefault()
                setDropTargetId(item.id)
                event.dataTransfer.dropEffect = 'move'
              }}
              onDrop={(event) => {
                event.preventDefault()
                const sourceId =
                  event.dataTransfer.getData('text/plain') || draggingId
                if (sourceId) {
                  const current = orderedItems.map(({ id, week }) => ({
                    id,
                    week,
                  }))
                  queue.updateItems(
                    moveMaterialQueueItem(current, sourceId, item.id),
                  )
                }
                setDraggingId(null)
                setDropTargetId(null)
              }}
              onDragLeave={() => setDropTargetId(null)}
            >
              <MaterialReorderControls
                name={item.file.name}
                disabled={disabled || queue.isPending || queue.preparing}
                isFirst={index === 0}
                isLast={index === orderedItems.length - 1}
                onMoveUp={() => moveItem(item.id, index - 1)}
                onMoveDown={() => moveItem(item.id, index + 1)}
              />
              <MaterialQueueCard
                item={item}
                kinds={kinds}
                disabled={disabled || queue.isPending || queue.preparing}
                dropTarget={dropTargetId === item.id}
                onWeekChange={(week) => queue.updateWeek(item.id, week)}
                onKindChange={(kind) => queue.updateKind(item.id, kind)}
                onRemove={() => queue.removeItem(item.id)}
                onSend={() => queue.sendItem(item.id)}
                sending={queue.sendingItemId === item.id}
              />
            </div>
          ))}
        </div>
      )}
      <MaterialUploadSubmit queue={queue} disabled={disabled} />
    </>
  )
}

export function MaterialUploadSubmit({
  queue,
  disabled = false,
}: {
  queue: MaterialUploadQueueState
  disabled?: boolean
}) {
  const { t } = useTranslation()
  if (queue.items.length === 0) return null
  return (
    <Button
      type="button"
      color="green"
      disabled={disabled || queue.isPending || !queue.valid}
      onClick={queue.sendAll}
    >
      {queue.isPending
        ? t('competencyMap.queueSending')
        : t('competencyMap.queueSendAll')}
    </Button>
  )
}

export function MaterialQueueCard({
  item,
  kinds,
  disabled,
  dropTarget,
  onWeekChange,
  onKindChange,
  onRemove,
  onSend,
  sending,
}: {
  item: MaterialQueueItem
  kinds: CourseMaterialKindOption[]
  disabled: boolean
  dropTarget: boolean
  onWeekChange: (week: string) => void
  onKindChange: (kind: MaterialQueueItem['kind']) => void
  onRemove: () => void
  onSend: () => void
  sending: boolean
}) {
  const { t } = useTranslation()
  const formattedSize =
    item.file.size > 1024 * 1024
      ? `${(item.file.size / (1024 * 1024)).toFixed(1)} МБ`
      : `${Math.round(item.file.size / 1024)} КБ`
  const canSend =
    /^\d+$/.test(item.week) &&
    Number(item.week) > 0 &&
    kinds.some((kind) => kind.code === item.kind)

  return (
    <CourseMaterialCard
      name={item.file.name}
      status={t(
        item.error
          ? 'competencyMap.queueItemError'
          : 'competencyMap.queueItemReady',
      )}
      statusColor={item.error ? 'red' : 'blue'}
      dropTarget={dropTarget}
      meta={t('competencyMap.materialsSize', { size: formattedSize })}
      actions={
        <Button
          type="button"
          size="1"
          variant="soft"
          color="red"
          disabled={disabled}
          onClick={onRemove}
          aria-label={t('competencyMap.queueRemove')}
          title={t('competencyMap.queueRemove')}
        >
          <Trash2 size={14} />
        </Button>
      }
    >
      {item.error && (
        <Text as="p" role="alert" color="red" size="1">
          {item.error}
        </Text>
      )}
      <MaterialMetadataFields
        week={item.week}
        kind={item.kind}
        kinds={kinds}
        disabled={disabled}
        onWeekChange={onWeekChange}
        onKindChange={(kind) => onKindChange(kind as MaterialQueueItem['kind'])}
        action={
          <Button
            type="button"
            size="2"
            variant="soft"
            color="green"
            disabled={disabled || !canSend}
            onClick={onSend}
            aria-label={t('competencyMap.queueSendOne')}
            title={t('competencyMap.queueSendOne')}
          >
            <Send size={14} />
            {sending
              ? t('competencyMap.queueSending')
              : t('competencyMap.queueSendOne')}
          </Button>
        }
      />
    </CourseMaterialCard>
  )
}
