<script setup lang="ts">
import { computed, ref, useAttrs, useId, useSlots, type Component } from 'vue'

// Every attribute belongs to the control, never to the layout box around it:
// an `aria-label` on a `<div>` describes nothing. The root still needs
// `inheritAttrs: false` or Vue would write them there as well.
defineOptions({ inheritAttrs: false })

/**
 * The console's single field: a Material-style outlined control with a notched
 * floating label and an optional suffix icon/action.
 *
 * At rest the label sits centered on the value line. On focus, or once there is a
 * value, it floats up into a notch on the top border and stays there. Focus
 * thickens the outline to 2 px in the signal colour, and an invalid field
 * keeps the error tone.
 *
 * A field whose name lives in the surrounding chrome — a search box in a
 * toolbar, a filter in a table header — sets `bare`: no caption, no notch, and
 * its name comes from `aria-label` through attribute fallthrough.
 *
 * Suffix icons/actions can be supplied via `suffixIcon` prop or the `#suffix` /
 * `#action` slots. When no suffix is present, the field retains balanced padding.
 */
const props = withDefaults(
  defineProps<{
    /** Visible caption. Required unless `bare`. */
    label?: string
    /** Which element to render. */
    as?: 'input' | 'textarea' | 'select'
    /** Height tier; matches the button size it sits beside. */
    size?: 'md' | 'sm' | 'xs'
    /** Input type; ignored by textarea and select. */
    type?: string
    /** Drops the caption and its reserved line, for chrome-level controls. */
    bare?: boolean
    /** Draws the field in the error tone and marks the control invalid. */
    invalid?: boolean
    /** Helper text under the field, in the error tone when `invalid`. */
    hint?: string
    /** Tints the hint without marking the control invalid. */
    hintTone?: 'warning'
    placeholder?: string
    disabled?: boolean
    /** Visible lines for a textarea; ignored by the other elements. */
    rows?: number
    /** Extra classes for the control itself, e.g. `font-mono`. */
    controlClass?: string
    /** Extra classes for the wrapper, for spacing at the call site. */
    rootClass?: string
    /** Optional suffix icon component. */
    suffixIcon?: Component | string
    /** Vue's `.number` modifier on a component `v-model`. */
    modelModifiers?: { number?: boolean }
  }>(),
  { as: 'input', size: 'sm', type: 'text' },
)

const model = defineModel<string | number>({ default: '' })

const attrs = useAttrs()
const slots = useSlots()

const generatedId = useId()
/**
 * The caller's `id` wins, so an existing `<label for>` in a view keeps
 * pointing at the control; otherwise the field generates one and its own
 * caption points at that.
 */
const controlId = computed(() =>
  typeof attrs.id === 'string' && attrs.id !== '' ? attrs.id : generatedId,
)

const control = ref<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement | null>(null)

/** Whether the control has a value the caption must clear. */
const filled = computed(() => {
  // A select's first option is a real choice, and a textarea has no single
  // value line for a caption to rest on, so both always show the caption.
  if (props.as === 'select' || props.as === 'textarea') return true
  const value = model.value
  return value !== '' && value !== null && value !== undefined
})

/**
 * A caption exists only when the field has one and is not `bare`.
 */
const hasLabel = computed(() => Boolean(props.label) && !props.bare)

const floated = computed(() => filled.value || !hasLabel.value)

/** Whether a suffix icon or action slot is provided. */
const hasSuffix = computed(
  () => Boolean(props.suffixIcon) || Boolean(slots.suffix) || Boolean(slots.action),
)

/**
 * Writes the control's value back through `v-model`. A select with `.number`
 * (the status and contact-type pickers) needs the coercion Vue would have
 * applied itself, because the inner element is bound by hand rather than with
 * `v-model`.
 */
function commit(event: Event) {
  const target = event.target as HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement
  const raw = target.value
  model.value = props.modelModifiers?.number ? Number(raw) : raw
}

defineExpose({
  focus: () => control.value?.focus(),
  el: control,
})
</script>

<template>
  <div
    :class="[
      'field',
      `field-${props.size}`,
      props.rootClass,
      {
        'field-bare': !hasLabel,
        'is-floated': floated,
        'is-invalid': props.invalid,
        'is-disabled': props.disabled,
        'has-suffix': hasSuffix,
      },
    ]"
  >
    <div class="field-box">
      <!-- Outlined border notch container -->
      <fieldset aria-hidden="true" class="field-outline">
        <legend v-if="hasLabel" class="field-legend">
          <span>{{ props.label }}</span>
        </legend>
      </fieldset>

      <label v-if="hasLabel" :for="controlId" class="field-label-float">
        {{ props.label }}
      </label>

      <textarea
        v-if="props.as === 'textarea'"
        :id="controlId"
        ref="control"
        v-bind="$attrs"
        :rows="props.rows ?? 3"
        :class="['field-control', props.controlClass]"
        :value="model"
        :placeholder="props.placeholder"
        :disabled="props.disabled"
        :aria-invalid="props.invalid || undefined"
        @input="commit"
      />

      <select
        v-else-if="props.as === 'select'"
        :id="controlId"
        ref="control"
        v-bind="$attrs"
        :class="['field-control', props.controlClass]"
        :value="model"
        :disabled="props.disabled"
        :aria-invalid="props.invalid || undefined"
        @change="commit"
      >
        <slot />
      </select>

      <input
        v-else
        :id="controlId"
        ref="control"
        v-bind="$attrs"
        :class="['field-control', props.controlClass]"
        :type="props.type"
        :value="model"
        :placeholder="props.placeholder"
        :disabled="props.disabled"
        :aria-invalid="props.invalid || undefined"
        @input="commit"
      />

      <div v-if="props.suffixIcon || $slots.suffix" class="field-suffix">
        <slot name="suffix">
          <component
            :is="props.suffixIcon"
            v-if="props.suffixIcon"
            class="size-4 shrink-0"
            aria-hidden="true"
          />
        </slot>
      </div>

      <slot name="action" />
    </div>

    <p
      v-if="props.hint"
      class="field-hint"
      :class="{
        'field-hint-error': props.invalid,
        'field-hint-warning': !props.invalid && props.hintTone === 'warning',
      }"
    >
      {{ props.hint }}
    </p>
  </div>
</template>
