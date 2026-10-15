import { useMemo } from 'react'
import * as THREE from 'three'

const vertex = /* glsl */ `
  varying vec3 vWorld;
  void main() {
    vec4 w = modelMatrix * vec4(position, 1.0);
    vWorld = w.xyz;
    gl_Position = projectionMatrix * viewMatrix * w;
  }
`

const fragment = /* glsl */ `
  varying vec3 vWorld;
  uniform vec3 uColor;
  uniform vec3 uMajor;
  float grid(vec2 p, float size) {
    vec2 g = abs(fract(p / size - 0.5) - 0.5) / fwidth(p / size);
    return 1.0 - min(min(g.x, g.y), 1.0);
  }
  void main() {
    float d = length(vWorld.xz);
    float fade = 1.0 - smoothstep(6.0, 26.0, d);
    float minor = grid(vWorld.xz, 1.0) * 0.35;
    float major = grid(vWorld.xz, 5.0);
    vec3 c = mix(uColor * minor, uMajor, major * 0.6);
    float a = max(minor, major * 0.8) * fade;
    gl_FragColor = vec4(c, a);
  }
`

/** A lab floor: a fine grid that fades into the dark. */
export function Floor() {
  const material = useMemo(
    () =>
      new THREE.ShaderMaterial({
        vertexShader: vertex,
        fragmentShader: fragment,
        transparent: true,
        depthWrite: false,
        uniforms: {
          uColor: { value: new THREE.Color('#26303c') },
          uMajor: { value: new THREE.Color('#2f3a48') },
        },
      }),
    [],
  )
  return (
    <mesh rotation-x={-Math.PI / 2} position-y={-0.001} material={material}>
      <planeGeometry args={[90, 90]} />
    </mesh>
  )
}
