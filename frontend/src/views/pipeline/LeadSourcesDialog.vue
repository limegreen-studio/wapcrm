<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { leadsService, type LeadSources } from '@/services/api'
import { getErrorMessage, unwrapResponse } from '@/lib/api-utils'

const open = defineModel<boolean>('open', { default: false })
const { t } = useI18n()

const sources = ref<LeadSources | null>(null)
const pageToken = ref('')
const appSecret = ref('')
const isSaving = ref(false)
const confirmRotate = ref(false)

async function load() {
  confirmRotate.value = false
  pageToken.value = ''
  appSecret.value = ''
  try {
    sources.value = unwrapResponse<LeadSources>(await leadsService.sources())
  } catch (e) {
    toast.error(getErrorMessage(e, t('sources.failed')))
  }
}

watch(open, value => { if (value) load() })

const basePath = ((window as any).__BASE_PATH__ ?? '').replace(/\/$/, '')
const apiBase = computed(() => `${window.location.origin}${basePath}/api/public`)
const formUrl = computed(() => `${apiBase.value}/leads/${sources.value?.token ?? ''}`)
const facebookUrl = computed(() => `${apiBase.value}/facebook/${sources.value?.token ?? ''}`)

const widget = computed(() => `<form id="lead-form">
  <input name="name" placeholder="Name">
  <input name="phone" placeholder="Phone" required>
  <input name="company_website" style="display:none" tabindex="-1" autocomplete="off">
  <button>Send</button>
</form>
<script>
document.getElementById('lead-form').addEventListener('submit', function (e) {
  e.preventDefault();
  fetch('${formUrl.value}', { method: 'POST', body: new URLSearchParams(new FormData(e.target)) })
    .then(function () { e.target.reset(); });
});
</` + `script>`)

const googleForms = computed(() => `function onFormSubmit(e) {
  var values = e.namedValues;
  UrlFetchApp.fetch('${formUrl.value}', {
    method: 'post',
    payload: { name: values['Name'][0], phone: values['Phone'][0], lead_source: 'google_forms' }
  });
}`)

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    toast.success(t('sources.copied'))
  } catch {
    toast.error(t('sources.failed'))
  }
}

async function rotate() {
  if (!confirmRotate.value) {
    confirmRotate.value = true
    return
  }
  try {
    sources.value = unwrapResponse<LeadSources>(await leadsService.rotateSourceToken())
    confirmRotate.value = false
    toast.success(t('sources.rotated'))
  } catch (e) {
    toast.error(getErrorMessage(e, t('sources.failed')))
  }
}

async function saveFacebook() {
  isSaving.value = true
  try {
    sources.value = unwrapResponse<LeadSources>(await leadsService.updateSources({
      team_id: sources.value?.team_id,
      facebook_page_token: pageToken.value || undefined,
      facebook_app_secret: appSecret.value || undefined
    }))
    pageToken.value = ''
    appSecret.value = ''
    toast.success(t('sources.saved'))
  } catch (e) {
    toast.error(getErrorMessage(e, t('sources.failed')))
  } finally {
    isSaving.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-w-2xl max-h-[90vh] overflow-y-auto">
      <DialogHeader>
        <DialogTitle>{{ $t('sources.title') }}</DialogTitle>
        <DialogDescription>{{ $t('sources.description') }}</DialogDescription>
      </DialogHeader>

      <div v-if="sources" class="space-y-5 text-sm">
        <section class="space-y-1.5">
          <Label>{{ $t('sources.url') }}</Label>
          <div class="flex gap-2">
            <Input :model-value="formUrl" readonly class="font-mono text-xs" />
            <Button variant="outline" @click="copy(formUrl)">{{ $t('sources.copy') }}</Button>
          </div>
          <Button
            :variant="confirmRotate ? 'destructive' : 'ghost'"
            size="sm"
            class="h-7 px-2 text-xs"
            @click="rotate"
          >{{ confirmRotate ? $t('sources.confirmRotate') : $t('sources.rotate') }}</Button>
        </section>

        <section class="space-y-1.5">
          <Label>{{ $t('sources.widget') }}</Label>
          <Textarea :model-value="widget" readonly :rows="6" class="font-mono text-xs" />
          <Button variant="outline" size="sm" @click="copy(widget)">{{ $t('sources.copy') }}</Button>
        </section>

        <section class="space-y-1.5">
          <Label>{{ $t('sources.googleForms') }}</Label>
          <p class="text-xs text-muted-foreground">{{ $t('sources.googleFormsHint') }}</p>
          <Textarea :model-value="googleForms" readonly :rows="6" class="font-mono text-xs" />
          <Button variant="outline" size="sm" @click="copy(googleForms)">{{ $t('sources.copy') }}</Button>
        </section>

        <section class="space-y-2 rounded-md border p-3">
          <p class="font-medium">{{ $t('sources.facebook') }}</p>
          <p class="text-xs text-muted-foreground">{{ $t('sources.facebookHint') }}</p>
          <Input :model-value="facebookUrl" readonly class="font-mono text-xs" :aria-label="$t('sources.url')" />
          <div class="space-y-1.5">
            <Label for="fb-page-token">{{ $t('sources.pageToken') }}</Label>
            <Input id="fb-page-token" v-model="pageToken" type="password" autocomplete="off"
              :placeholder="sources.facebook_ready ? $t('sources.configured') : ''" />
          </div>
          <div class="space-y-1.5">
            <Label for="fb-app-secret">{{ $t('sources.appSecret') }}</Label>
            <Input id="fb-app-secret" v-model="appSecret" type="password" autocomplete="off"
              :placeholder="sources.facebook_ready ? $t('sources.configured') : ''" />
          </div>
          <DialogFooter>
            <Button :disabled="isSaving || (!pageToken && !appSecret)" @click="saveFacebook">{{ $t('common.save') }}</Button>
          </DialogFooter>
        </section>
      </div>
    </DialogContent>
  </Dialog>
</template>
