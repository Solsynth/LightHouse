// @carbon/icons-vue publishes runtime components without TypeScript
// declarations. Only the glyphs this console imports need typing.
declare module '@carbon/icons-vue' {
  import type { DefineComponent } from 'vue'

  export const Activity16: DefineComponent
  export const Api16: DefineComponent
  export const Checkmark16: DefineComponent
  export const ChevronLeft16: DefineComponent
  export const ChevronRight16: DefineComponent
  export const Close16: DefineComponent
  export const Copy16: DefineComponent
  export const Devices16: DefineComponent
  export const FingerprintRecognition16: DefineComponent
  export const Launch16: DefineComponent
  export const Moon16: DefineComponent
  export const Sun16: DefineComponent
  export const Tools16: DefineComponent
  export const User16: DefineComponent
  export const WarningAlt16: DefineComponent
}
