import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Boxes, Container, Download } from 'lucide-react'
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  LoadingState,
  TableWrap,
  tableClass,
  type Tone,
} from '@/components/ui'
import { useAction, useQuery } from '@/hooks/useApi'
import { api } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import { AreaCard, JobOutput, checkedText } from './shared'
import { t } from './strings'
import { refreshSummary } from './summary'
import type { DockerView, ImageState, ImageStatus, JobMeta } from './types'

const tone: Record<ImageState, Tone> = { current: 'success', update_available: 'warning', unchecked: 'neutral' }

function label(s: ImageState): string {
  return s === 'current' ? t('imgCurrent') : s === 'update_available' ? t('imgUpdate') : t('imgUnchecked')
}

export function DockerSection() {
  const isAdmin = useAuth((s) => s.isAdmin)
  const docker = useQuery<DockerView>('/updates/docker')
  const [target, setTarget] = useState<ImageStatus | null>(null)
  const [job, setJob] = useState<JobMeta | null>(null)

  const after = () => {
    void docker.reload()
    refreshSummary()
  }
  const check = useAction(() => api.post<DockerView>('/updates/docker/check'), {
    onSuccess: () => {
      toast.success(t('checkDone'))
      after()
    },
    onError: (m) => {
      toast.error(m)
      after()
    },
  })
  const pull = useAction((image: string) => api.post<JobMeta>('/updates/docker/pull', { image }), {
    onSuccess: (j) => {
      setJob(j)
      setTarget(null)
      toast.info(t('pullStarted'))
    },
  })

  const data = docker.data
  const activeJob = job ?? (data?.job?.status === 'running' ? data.job : null)
  const running = activeJob?.status === 'running'

  return (
    <AreaCard
      title={t('dockerTitle')}
      icon={Container}
      subtitle={data ? checkedText(data.checked_at, data.auto_check) : ''}
      badge={data && data.count > 0 ? <Badge tone="warning">{t('updatesAvailable', { count: data.count })}</Badge> : undefined}
      canCheck={isAdmin}
      checking={check.pending || Boolean(data?.checking)}
      onCheck={() => void check.run()}
      error={data?.error}
    >
      {docker.loading ? (
        <LoadingState />
      ) : docker.error || !data ? (
        <ErrorState message={docker.error ?? ''} onRetry={() => void docker.reload()} />
      ) : data.checked_at === null ? (
        <EmptyState icon={Boxes} title={t('dockerNeverTitle')} description={t('dockerNeverDesc')} />
      ) : data.images.length === 0 ? (
        <EmptyState icon={Boxes} title={t('dockerEmptyTitle')} description={t('dockerEmptyDesc')} />
      ) : (
        <div className="flex flex-col gap-4">
          {activeJob && isAdmin && (
            <JobOutput
              job={activeJob}
              onDone={(m) => {
                setJob(m)
                if (m.status === 'success') toast.success(t('pullOk'))
                else toast.error(m.message || t('pullFailed'))
                after()
              }}
            />
          )}
          {data.unchecked > 0 && <p className="text-xs text-muted">{t('uncheckedCount', { count: data.unchecked })}</p>}
          <TableWrap>
            <table className={tableClass.table}>
              <thead>
                <tr>
                  <th className={tableClass.th}>{t('colImage')}</th>
                  <th className={tableClass.th}>{t('colUsedBy')}</th>
                  <th className={tableClass.th}>{t('colStatus')}</th>
                  <th className={tableClass.th}>{t('colAction')}</th>
                </tr>
              </thead>
              <tbody>
                {data.images.map((img) => (
                  <tr key={img.ref + img.containers.map((c) => c.id).join()} className={tableClass.row}>
                    <td className={tableClass.td}>
                      <span className="font-medium break-all">{img.ref}</span>
                      {img.app && (
                        <div className="mt-1">
                          <Badge tone="accent">
                            {t('colApp')}: {img.app}
                          </Badge>
                        </div>
                      )}
                    </td>
                    <td className={tableClass.td + ' text-xs text-muted'}>
                      <span className="break-words">{img.containers.map((c) => c.name || c.id).join(', ')}</span>
                    </td>
                    <td className={tableClass.td}>
                      <div className="flex flex-wrap items-center gap-1.5">
                        <Badge tone={tone[img.status]}>{label(img.status)}</Badge>
                        {img.pulled && <Badge tone="cyan">{t('imgPulled')}</Badge>}
                      </div>
                      {img.status === 'unchecked' && img.reason && <p className="mt-1 max-w-xs text-xs text-faint">{img.reason}</p>}
                      {img.pulled && <p className="mt-1 max-w-xs text-xs text-faint">{t('imgPulledHint')}</p>}
                    </td>
                    <td className={tableClass.td}>
                      {img.status === 'update_available' ? (
                        <div className="flex flex-wrap items-center gap-2">
                          {isAdmin && !img.pulled && (
                            <Button
                              size="md"
                              icon={Download}
                              disabled={running}
                              onClick={() => {
                                pull.clearError()
                                setTarget(img)
                              }}
                            >
                              {t('pull')}
                            </Button>
                          )}
                          <Link
                            to={img.app ? '/apps' : '/docker'}
                            className="inline-flex min-h-10 items-center text-xs font-medium text-accent hover:underline"
                          >
                            {img.app ? t('openApps') : t('openDocker')}
                          </Link>
                        </div>
                      ) : (
                        <span className="text-xs text-faint">—</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
          {data.count > 0 && <Alert tone="accent">{t('dockerNote')}</Alert>}
        </div>
      )}

      <ConfirmDialog
        open={target !== null}
        onClose={() => setTarget(null)}
        onConfirm={async () => {
          if (target) await pull.run(target.ref)
        }}
        title={t('pullConfirmTitle')}
        message={t('pullConfirmMessage', { image: target?.ref ?? '' })}
        confirmLabel={t('pull')}
        cancelLabel={t('cancel')}
        pending={pull.pending}
        error={pull.error}
      />
    </AreaCard>
  )
}
