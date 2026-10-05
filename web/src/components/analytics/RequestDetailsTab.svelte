<script lang="ts">
  import { RefreshCw, X } from 'lucide-svelte'
  import Badge from '../../lib/ui/Badge.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import Card from '../../lib/ui/Card.svelte'
  import { getIconPath } from '../connections/types'
  import { cachedTokensFor, calculateTPS, fmt, formatDuration, providerDisplayName, timeAgo, type RequestDetailItem } from './types'

interface Props {
  details?: RequestDetailItem[]
  detailsTotal?: number
  detailsPage?: number
  detailsLoading?: boolean
  onPageChange: (page: number) => void
  onRefresh: () => void
  /** Custom provider nodes, so a synthetic id renders as its configured name. */
  providerNodes?: { id: string; name?: string }[]
}

let {
  details = [],
  detailsTotal = 0,
  detailsPage = 1,
  detailsLoading = false,
  onPageChange,
  onRefresh,
  providerNodes = [],
}: Props = $props()

  let selectedDetail = $state<RequestDetailItem | null>(null)

</script>

<Card padding="none" class="overflow-hidden border border-border">
  <div class="px-5 py-3 border-b border-border flex items-center justify-between bg-surface-2">
    <div>
      <h2 class="font-headline text-sm font-bold text-text-main">Recent Request Details</h2>
      <p class="font-body text-xs text-text-muted">Total recorded: {detailsTotal.toLocaleString()} requests</p>
    </div>
    <Button variant="secondary" size="sm" onclick={onRefresh} disabled={detailsLoading}>
      <RefreshCw class="w-3.5 h-3.5 mr-1 {detailsLoading ? 'animate-spin' : ''}" />
      Refresh
    </Button>
  </div>

  {#if detailsLoading}
    <div class="p-12 text-center text-text-muted text-sm flex items-center justify-center gap-2">
      <span class="w-4 h-4 border-2 border-brand-500 border-t-transparent rounded-full animate-spin"></span>
      <span>Loading request history...</span>
    </div>
  {:else if details.length === 0}
    <div class="p-12 text-center text-text-muted text-sm">
      No request logs found in the database.
    </div>
  {:else}
    <div class="overflow-x-auto">
      <table class="w-full text-left border-collapse text-xs font-body">
        <thead class="bg-surface-2 border-b border-border text-text-muted uppercase text-[10px] font-semibold tracking-wider">
          <tr>
            <th class="py-3 px-4 w-2"></th>
            <th class="py-3 px-4">Time</th>
            <th class="py-3 px-4">Provider</th>
            <th class="py-3 px-4">Model</th>
            <th class="py-3 px-4 text-right">TTFT</th>
            <th class="py-3 px-4 text-right">Duration</th>
            <th class="py-3 px-4 text-right">Prompt</th>
            <th class="py-3 px-4 text-right">Completion</th>
            <th class="py-3 px-4 text-right">Cached</th>
            <th class="py-3 px-4 text-right">Actions</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border/60 font-code text-[11px]">
          {#each details as item}
            <tr class="hover:bg-surface-2 transition-colors cursor-pointer" onclick={() => (selectedDetail = item)}>
              <td class="py-3 px-4">
                <span class="block w-2 h-2 rounded-full {item.status === 'success' || item.status === 'ok' ? 'bg-success' : 'bg-error'}"></span>
              </td>
              <td class="py-3 px-4 text-text-muted whitespace-nowrap text-[11px]">
                {timeAgo(item.timestamp)}
              </td>
              <td class="py-3 px-4">
                <div class="flex items-center gap-1.5">
                  {#if item.provider}
                    <img
                      src={getIconPath(item.provider)}
                      alt={item.provider}
                      class="w-3.5 h-3.5 object-contain rounded shrink-0"
                      onerror={(e) => {
                        (e.currentTarget as HTMLElement).style.display = 'none'
                      }}
                      loading="lazy"
                    />
                  {/if}
                  <span title={item.provider || undefined}>
                    <Badge variant="neutral" size="sm">{providerDisplayName(item.provider, providerNodes)}</Badge>
                  </span>
                </div>
              </td>
              <td class="py-3 px-4 font-bold text-text-main max-w-[140px] truncate">
                <div class="flex items-center gap-1.5">
                  {#if item.provider}
                    <img
                      src={getIconPath(item.provider)}
                      alt={item.model}
                      class="w-3.5 h-3.5 object-contain rounded shrink-0 bg-surface-2 p-0.5 border border-border/40"
                      onerror={(e) => {
                        (e.currentTarget as HTMLElement).style.display = 'none'
                      }}
                      loading="lazy"
                    />
                  {/if}
                  <span class="truncate">{item.model}</span>
                </div>
              </td>
              <td class="py-3 px-4 text-right text-text-muted">
                {item.latency?.ttft && item.latency.ttft > 0 ? formatDuration(item.latency.ttft) : '—'}
              </td>
              <td class="py-3 px-4 text-right text-text-main font-medium" title={item.latency?.total ? `${item.latency.total}ms` : undefined}>
                {formatDuration(item.latency?.total)}
              </td>
              <td class="py-3 px-4 text-right text-brand-500 whitespace-nowrap">
                <div class="inline-flex items-center justify-end gap-1.5">
                  <span>{fmt(item.tokens?.prompt_tokens)}</span>
                  {#if item.tokens?.saved_tokens && item.tokens.saved_tokens > 0}
                    <span title={`Saved ${fmt(item.tokens.saved_tokens)} tokens`}>
                      <Badge variant="success" size="sm" class="text-[10px] px-1.5 py-0 font-semibold">
                        {item.tokens.saved_percent ? `-${item.tokens.saved_percent}% RTK` : `saved ${fmt(item.tokens.saved_tokens)}`}
                      </Badge>
                    </span>
                  {/if}
                </div>
              </td>
              <td class="py-3 px-4 text-right text-success">
                {fmt(item.tokens?.completion_tokens)}
              </td>
              <td class="py-3 px-4 text-right text-info">
                {fmt(cachedTokensFor(item))}
              </td>
              <td class="py-3 px-4 text-right">
                <button
                  type="button"
                  onclick={(e) => {
                    e.stopPropagation()
                    selectedDetail = item
                  }}
                  class="text-xs text-brand-500 hover:underline font-semibold cursor-pointer"
                >
                  View
                </button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>

    <!-- Pagination Controls -->
    <div class="px-4 py-3 border-t border-border flex items-center justify-between text-xs text-text-muted bg-surface-2">
      <span>Showing page {detailsPage} of {Math.ceil(detailsTotal / 20) || 1}</span>
      <div class="flex items-center gap-2">
        <Button
          variant="secondary"
          size="sm"
          disabled={detailsPage <= 1}
          onclick={() => onPageChange(detailsPage - 1)}
        >
          Previous
        </Button>
        <Button
          variant="secondary"
          size="sm"
          disabled={detailsPage * 20 >= detailsTotal}
          onclick={() => onPageChange(detailsPage + 1)}
        >
          Next
        </Button>
      </div>
    </div>
  {/if}
</Card>

<!-- Slide-over Request Inspector Modal -->
{#if selectedDetail}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/60 backdrop-blur-sm">
    <div class="w-full max-w-2xl max-h-[85vh] rounded-2xl bg-surface border border-border shadow-2xl flex flex-col overflow-hidden">
      <!-- Modal Header -->
      <div class="px-6 py-4 border-b border-border flex items-center justify-between bg-surface-2">
        <div class="flex items-center gap-2">
          <span class="w-2.5 h-2.5 rounded-full {selectedDetail.status === 'success' ? 'bg-success' : 'bg-error'}"></span>
          <h3 class="font-headline text-base font-bold text-text-main">{selectedDetail.model}</h3>
          <span title={selectedDetail.provider || undefined}>
            <Badge variant="neutral" size="sm">{providerDisplayName(selectedDetail.provider, providerNodes)}</Badge>
          </span>
        </div>
        <button
          type="button"
          onclick={() => (selectedDetail = null)}
          class="p-1 rounded-lg text-text-muted hover:text-text-main hover:bg-surface-3 transition-colors cursor-pointer"
        >
          <X class="w-5 h-5" />
        </button>
      </div>

      <!-- Modal Body -->
      <div class="flex-1 overflow-y-auto p-6 space-y-4 font-body text-xs">
        <!-- Top Metrics / Execution Summary (OmniRoute style) -->
        <div class="grid grid-cols-2 sm:grid-cols-4 gap-3">
          <div class="p-3 rounded-lg bg-surface-2 border border-border">
            <div class="text-text-muted text-[10px] uppercase font-bold tracking-wider">Duration</div>
            <div class="font-code text-sm font-bold text-text-main mt-1" title={selectedDetail.latency?.total ? `${selectedDetail.latency.total}ms` : undefined}>
              {formatDuration(selectedDetail.latency?.total)}
            </div>
          </div>
          <div class="p-3 rounded-lg bg-surface-2 border border-border">
            <div class="text-text-muted text-[10px] uppercase font-bold tracking-wider">TTFT</div>
            <div class="font-code text-sm font-bold text-text-main mt-1" title={selectedDetail.latency?.ttft ? `${selectedDetail.latency.ttft}ms` : undefined}>
              {selectedDetail.latency?.ttft && selectedDetail.latency.ttft > 0 ? formatDuration(selectedDetail.latency.ttft) : '— (non-stream)'}
            </div>
          </div>
          <div class="p-3 rounded-lg bg-surface-2 border border-border">
            <div class="text-text-muted text-[10px] uppercase font-bold tracking-wider">Speed</div>
            <div class="font-code text-sm font-bold text-emerald-500 mt-1">
              {calculateTPS(selectedDetail.tokens?.completion_tokens, selectedDetail.latency?.total, selectedDetail.latency?.ttft) || '—'}
            </div>
          </div>
          <div class="p-3 rounded-lg bg-surface-2 border border-border">
            <div class="text-text-muted text-[10px] uppercase font-bold tracking-wider">Status</div>
            <div class="font-code text-sm font-bold {selectedDetail.status === 'success' || selectedDetail.status === 'ok' ? 'text-success' : 'text-error'} mt-1">
              {selectedDetail.status === 'success' || selectedDetail.status === 'ok' ? '200 OK' : (selectedDetail.status || 'Error')}
            </div>
          </div>
        </div>

        {#if selectedDetail.tokens}
          {@const promptTokens = selectedDetail.tokens.prompt_tokens ?? 0}
          {@const cacheRead = cachedTokensFor(selectedDetail)}
          {@const savedTokens = selectedDetail.tokens.saved_tokens ?? 0}
          {@const fromTokens = savedTokens > 0 ? (promptTokens + savedTokens) : promptTokens}
          {@const savedPct = fromTokens > 0 ? Math.round((savedTokens / fromTokens) * 100) : 0}
          {@const compTokens = selectedDetail.tokens.completion_tokens ?? 0}
          {@const reasoningTokens = selectedDetail.tokens.reasoning_tokens ?? 0}

          <!-- Token Group: Input (OmniRoute style) -->
          <div class="p-3.5 rounded-xl bg-surface-2/60 border border-border space-y-1.5">
            <div class="text-[10px] text-text-muted uppercase font-bold tracking-wider">
              Input
            </div>
            <div class="flex flex-wrap items-center gap-1.5 font-code">
              <span class="px-2 py-0.5 rounded bg-brand-500/20 text-brand-500 text-xs font-bold">
                Total In: {fmt(promptTokens)}
              </span>
              <span class="px-2 py-0.5 rounded bg-sky-500/20 text-sky-700 dark:text-sky-400 text-xs font-bold">
                Cache Read: {cacheRead > 0 ? fmt(cacheRead) : '0'}
                {#if promptTokens > 0 && cacheRead > 0}
                  <span class="font-normal opacity-85 font-sans text-[11px]">({Math.min(100, Math.round((cacheRead / promptTokens) * 100))}%)</span>
                {/if}
              </span>
              <span class="px-2 py-0.5 rounded bg-amber-500/20 text-amber-700 dark:text-amber-400 text-xs font-bold">
                Cache Write: {selectedDetail.tokens.cache_creation_input_tokens ? fmt(selectedDetail.tokens.cache_creation_input_tokens) : 'N/A'}
              </span>
              {#if savedTokens > 0}
                <span class="px-2 py-0.5 rounded bg-purple-500/20 text-purple-700 dark:text-purple-300 text-xs font-bold">
                  Compressed: {fmt(fromTokens)} → {fmt(promptTokens)} ({savedPct}% saved)
                </span>
              {/if}
            </div>
          </div>

          <!-- Token Group: Output (OmniRoute style) -->
          <div class="p-3.5 rounded-xl bg-surface-2/60 border border-border space-y-1.5">
            <div class="text-[10px] text-text-muted uppercase font-bold tracking-wider">
              Output
            </div>
            <div class="flex flex-wrap items-center gap-1.5 font-code">
              <span class="px-2 py-0.5 rounded bg-emerald-500/20 text-emerald-700 dark:text-emerald-400 text-xs font-bold">
                Total Out: {fmt(compTokens)}
              </span>
              {#if reasoningTokens > 0}
                <span class="px-2 py-0.5 rounded bg-violet-500/20 text-violet-700 dark:text-violet-400 text-xs font-bold">
                  Reasoning: {fmt(reasoningTokens)}
                </span>
              {/if}
            </div>
          </div>
        {/if}

        <!-- Request Metadata -->
        <div class="grid grid-cols-2 sm:grid-cols-3 gap-2.5 pt-1">
          <div class="p-2.5 rounded-lg bg-surface-2 border border-border space-y-1">
            <div class="text-text-muted text-[10px] uppercase font-bold tracking-wider">Time</div>
            <div class="font-code text-xs text-text-main truncate">
              {timeAgo(selectedDetail.timestamp)}
            </div>
          </div>
          <div class="p-2.5 rounded-lg bg-surface-2 border border-border space-y-1">
            <div class="text-text-muted text-[10px] uppercase font-bold tracking-wider">Provider</div>
            <div class="font-code text-xs text-text-main truncate">
              {providerDisplayName(selectedDetail.provider, providerNodes)}
            </div>
          </div>
          <div class="p-2.5 rounded-lg bg-surface-2 border border-border space-y-1 col-span-2 sm:col-span-1">
            <div class="text-text-muted text-[10px] uppercase font-bold tracking-wider">Request ID</div>
            <div class="font-code text-xs text-text-muted truncate select-all" title={selectedDetail.id}>
              {selectedDetail.id || '—'}
            </div>
          </div>
        </div>

        <!-- Raw JSON details inspector -->
        <div class="space-y-1.5">
          <span class="font-semibold text-text-main uppercase text-[10px] tracking-wider">Payload</span>
          <pre class="p-4 rounded-xl bg-bg border border-border font-code text-[11px] text-text-main overflow-x-auto max-h-80 leading-relaxed">
{JSON.stringify(selectedDetail, null, 2)}
          </pre>
        </div>
      </div>

      <!-- Modal Footer -->
      <div class="px-6 py-3 border-t border-border bg-surface-2 flex justify-end">
        <Button variant="secondary" size="sm" onclick={() => (selectedDetail = null)}>
          Close
        </Button>
      </div>
    </div>
  </div>
{/if}
