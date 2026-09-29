/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { ViewIcon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import type { ColumnDef } from '@tanstack/react-table'
/* eslint-disable react-refresh/only-export-components */
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { getUserAvatarFallback, getUserAvatarStyle } from '@/lib/avatar'
import { formatTimestampToDate } from '@/lib/format'
import { cn } from '@/lib/utils'

import { taskActionMapper, taskStatusMapper } from '../../lib/mappers'
import type { TaskLog } from '../../types'
import { PromptDialog } from '../dialogs/prompt-dialog'
import { TaskDetailsDialog } from '../dialogs/task-details-dialog'
import { LogCostDisplay } from '../log-cost-display'
import { ModelBadge } from '../model-badge'
import { PluginAuthorLink } from '../plugin-author-link'
import { TaskArtifactsCell } from '../task-artifacts'
import { useUsageLogsContext } from '../usage-logs-provider'
import {
  createDurationColumn,
  createChannelColumn,
  createProgressColumn,
} from './column-helpers'

function TaskDetailsCell(props: {
  log: TaskLog
  isAdmin: boolean
  isRoot: boolean
}) {
  const { t } = useTranslation()
  const [dialogOpen, setDialogOpen] = useState(false)

  return (
    <>
      <div className='flex max-w-[220px] flex-col items-start gap-1'>
        <button
          type='button'
          className='text-foreground inline-flex items-center gap-1 text-xs font-medium hover:underline'
          onClick={() => setDialogOpen(true)}
        >
          <HugeiconsIcon
            icon={ViewIcon}
            className='size-3'
            strokeWidth={2}
            aria-hidden='true'
          />
          {t('View details')}
        </button>
        {props.log.fail_reason ? (
          <span className='max-w-full truncate text-xs text-red-600 dark:text-red-400'>
            {props.log.fail_reason}
          </span>
        ) : null}
      </div>
      <TaskDetailsDialog
        log={props.log}
        isAdmin={props.isAdmin}
        isRoot={props.isRoot}
        open={dialogOpen}
        onOpenChange={setDialogOpen}
      />
    </>
  )
}

function TaskPromptCell(props: { prompt?: string }) {
  const { t } = useTranslation()
  const [dialogOpen, setDialogOpen] = useState(false)
  if (!props.prompt) {
    return <span className='text-muted-foreground/60 text-xs'>-</span>
  }

  return (
    <>
      <button
        type='button'
        className='group flex max-w-[220px] items-center text-left text-xs'
        onClick={() => setDialogOpen(true)}
        title={t('Click to view full prompt')}
      >
        <span className='text-muted-foreground truncate leading-snug group-hover:underline'>
          {props.prompt}
        </span>
      </button>
      <PromptDialog
        prompt={props.prompt}
        open={dialogOpen}
        onOpenChange={setDialogOpen}
      />
    </>
  )
}

export function useTaskLogsColumns(
  isAdmin: boolean,
  isRoot: boolean
): ColumnDef<TaskLog>[] {
  const { t } = useTranslation()
  const columns: ColumnDef<TaskLog>[] = [
    {
      accessorKey: 'submit_time',
      header: t('Submit Time'),
      cell: ({ row }) => {
        const log = row.original
        const submitTime = row.getValue('submit_time') as number

        return (
          <div className='flex min-w-0 flex-col gap-0.5'>
            <span className='truncate font-mono text-xs tabular-nums'>
              {formatTimestampToDate(submitTime, 'seconds')}
            </span>
            {log.finish_time ? (
              <span className='text-muted-foreground/60 truncate font-mono text-[11px] tabular-nums'>
                {formatTimestampToDate(log.finish_time, 'seconds')}
              </span>
            ) : (
              <span className='text-muted-foreground/50 text-[11px]'>-</span>
            )}
          </div>
        )
      },
      size: 180,
    },
  ]

  if (isAdmin) {
    columns.push(
      createChannelColumn<TaskLog>({ headerLabel: t('Channel') }),
      {
        id: 'user',
        header: t('User'),
        accessorFn: (row) => row.username || row.user_id,
        cell: function UserCell({ row }) {
          const { sensitiveVisible, setSelectedUserId, setUserInfoDialogOpen } =
            useUsageLogsContext()
          const log = row.original
          const displayName = log.username || String(log.user_id || '?')

          return (
            <button
              type='button'
              className='flex items-center gap-1.5 text-left'
              onClick={(e) => {
                e.stopPropagation()
                setSelectedUserId(log.user_id)
                setUserInfoDialogOpen(true)
              }}
            >
              <Avatar className='ring-border/60 size-6 ring-1 max-sm:hidden'>
                <AvatarFallback
                  className={cn(
                    'text-[11px] font-semibold',
                    !sensitiveVisible && 'bg-muted text-muted-foreground'
                  )}
                  style={
                    sensitiveVisible
                      ? getUserAvatarStyle(displayName)
                      : undefined
                  }
                >
                  {sensitiveVisible ? getUserAvatarFallback(displayName) : '•'}
                </AvatarFallback>
              </Avatar>
              <span className='text-muted-foreground truncate text-sm hover:underline'>
                {sensitiveVisible ? displayName : '••••'}
              </span>
            </button>
          )
        },
      },
      {
        id: 'plugin',
        header: t('Plugin'),
        accessorFn: (row) => row.admin_info?.task_plugin?.key ?? '',
        cell: ({ row }) => {
          const plugin = row.original.admin_info?.task_plugin
          if (!plugin) {
            return <span className='text-muted-foreground/60 text-xs'>-</span>
          }
          return (
            <div className='flex max-w-[170px] flex-col gap-0.5'>
              <span className='truncate text-xs font-medium'>
                {plugin.name || plugin.key}
              </span>
              <span className='text-muted-foreground truncate font-mono text-[11px]'>
                {plugin.key}
                {plugin.version ? ` @ ${plugin.version}` : ''}
              </span>
              {plugin.author ? (
                <PluginAuthorLink
                  author={plugin.author}
                  showUrl
                  className='text-muted-foreground text-[11px]'
                />
              ) : null}
            </div>
          )
        },
      }
    )
  }

  columns.push(
    {
      id: 'type',
      header: t('Type'),
      accessorFn: (row) => row.platform,
      cell: ({ row }) => (
        <div className='flex max-w-[150px] flex-col gap-1'>
          <span className='truncate text-xs font-medium'>
            {t(row.original.platform)}
          </span>
          <span className='text-muted-foreground truncate text-[11px]'>
            {t(taskActionMapper.getLabel(row.original.action))}
          </span>
        </div>
      ),
      size: 120,
      maxSize: 160,
    },
    {
      id: 'model',
      header: t('Model'),
      accessorFn: (row) =>
        row.properties?.origin_model_name ||
        row.properties?.upstream_model_name ||
        '',
      cell: ({ row }) => {
        const model =
          row.original.properties?.origin_model_name ||
          row.original.properties?.upstream_model_name
        const actualModel = row.original.properties?.upstream_model_name
        if (!model) {
          return <span className='text-muted-foreground/60 text-xs'>-</span>
        }
        return (
          <ModelBadge
            modelName={model}
            actualModel={
              actualModel && actualModel !== model ? actualModel : undefined
            }
            className='max-w-[220px]'
          />
        )
      },
      size: 190,
      maxSize: 240,
    },
    {
      id: 'prompt',
      header: t('Prompt'),
      accessorFn: (row) => row.properties?.input || '',
      cell: ({ row }) => (
        <TaskPromptCell prompt={row.original.properties?.input} />
      ),
      size: 200,
      maxSize: 240,
    },
    {
      accessorKey: 'task_id',
      header: t('Task ID'),
      cell: ({ row }) => {
        const taskId = row.getValue('task_id') as string
        if (!taskId) {
          return <span className='text-muted-foreground/60 text-xs'>-</span>
        }
        return (
          <div className='flex max-w-[170px] flex-col gap-0.5'>
            <StatusBadge
              label={taskId}
              copyText={taskId}
              variant='neutral'
              size='sm'
              className='border-border/60 bg-muted/30 !text-foreground max-w-full truncate rounded-md border px-1.5 py-0.5 font-mono'
            />
          </div>
        )
      },
      meta: { mobileTitle: true },
    },
    createDurationColumn<TaskLog>({
      submitTimeKey: 'submit_time',
      finishTimeKey: 'finish_time',
      unit: 'seconds',
      headerLabel: t('Duration'),
      warningThresholdSec: 300,
    }),
    {
      accessorKey: 'status',
      header: t('Status'),
      cell: ({ row }) => {
        const status = row.getValue('status') as string
        return (
          <StatusBadge
            label={t(taskStatusMapper.getLabel(status, status || 'Submitting'))}
            variant={taskStatusMapper.getVariant(status)}
            size='sm'
            copyable={false}
            className='-ml-1.5'
          />
        )
      },
    },
    {
      accessorKey: 'quota',
      header: t('Cost'),
      cell: ({ row }) => (
        <LogCostDisplay quota={row.original.quota} other={null} />
      ),
      size: 110,
      maxSize: 140,
    },
    createProgressColumn<TaskLog>({ headerLabel: t('Progress') }),
    {
      id: 'artifacts',
      header: t('Artifacts'),
      cell: ({ row }) => (
        <TaskArtifactsCell key={row.original.task_id} log={row.original} />
      ),
      size: 120,
      maxSize: 140,
    },
    {
      accessorKey: 'fail_reason',
      header: t('Details'),
      cell: ({ row }) => (
        <TaskDetailsCell
          key={row.original.task_id}
          log={row.original}
          isAdmin={isAdmin}
          isRoot={isRoot}
        />
      ),
      size: 220,
      maxSize: 240,
    }
  )

  return columns
}
