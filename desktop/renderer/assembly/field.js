/* Three depth planes share the assembly clock so pause and reduced motion apply to the entire scene. */
function createSignalField(T, scene) {
  const uniforms = {
    uTime:{value:0}, uScroll:{value:0}, uPointer:{value:new T.Vector2()},
    uFocus:{value:new T.Vector2(.72,.5)}, uAspect:{value:1}
  };
  const background = new T.Mesh(new T.PlaneGeometry(2,2),new T.ShaderMaterial({
    uniforms, transparent:true, depthTest:false, depthWrite:false,
    vertexShader:`
      varying vec2 uvScreen;
      void main(){uvScreen=uv;gl_Position=vec4(position.xy,1.,1.);}
    `,
    fragmentShader:`
      varying vec2 uvScreen;
      uniform float uTime,uScroll,uAspect;
      uniform vec2 uPointer,uFocus;
      float hash(vec2 a){return fract(sin(dot(a,vec2(127.1,311.7)))*43758.5453);}
      float dust(vec2 p,float freq,float density){
        p*=freq;
        vec2 cell=floor(p), q=fract(p)-vec2(.16+.65*hash(cell),.16+.65*hash(cell+6.7));
        float radius=length(q);
        return step(density,hash(cell+1.17))*(1.-smoothstep(.012,.092,radius));
      }
      float lineAt(float p){
        float a=abs(fract(p+.5)-.5);
        return 1.-smoothstep(.012,.023+min(fwidth(p),.33),a);
      }
      vec3 sheet(vec2 p, float travel, float strength) {
        float bend = p.x*3.8 - travel;
        float crest = sin(bend + .36*sin(p.x*2.3 - travel*.56));
        float center = .15 + .112*crest + .043*sin(p.x*7.15 - travel*1.57) + .055*p.x;
        float width = .10 + .037*sin(p.x*3.2 - travel*.82 + .6);
        float v = (p.y - center)/width;
        float envelope = exp(-v*v*1.55);

        // Vary displacement across the ribbon so its strands fan out and gather as a crest passes.
        float shear = .17*sin(p.x*5.3 - travel*1.31 + v*1.25)
                    + .082*sin(p.x*8.2 - travel*1.78 - v*1.05);
        float strand = (v + shear)*13.5;
        float widthPx = max(fwidth(strand),.035);
        float filament = 1.-smoothstep(.023,.023+min(widthPx,.40),abs(fract(strand+.5)-.5));
        filament = mix(filament,.13,smoothstep(.45,1.,widthPx));

        float current = p.x*9.8 + v*.88 + .55*crest - travel*2.65;
        float glint = pow(.5+.5*cos(current),7.);
        float core = exp(-4.*v*v);
        vec3 silk = mix(vec3(.17,.28,.59),vec3(.53,.64,.98),glint);
        return strength*envelope*(silk*filament*(.30+.37*glint)
               + vec3(.05,.09,.23)*core*(.55+.45*glint));
      }
      void main(){
        vec2 uv=uvScreen;
        vec2 far=uv*vec2(uAspect,1.)+uPointer*.012+vec2(0.,uScroll*.016);
        vec2 near=uv*vec2(uAspect,1.)+uPointer*.037+vec2(0.,uScroll*.057);
        float phase=.21*sin(uTime*.085)+uScroll*.25;
        vec2 ff=(uv-uFocus)*vec2(uAspect,1.)-uPointer*.016;
        vec3 c=vec3(0.);
        c+=vec3(.067,.096,.215)*exp(-dot(ff,ff)*3.3);
        float beam=far.x*.69+far.y-.78-.20*sin(far.x*2.+phase*.48);
        c+=vec3(.034,.047,.118)*exp(-beam*beam*26.);
        float drift = sin(uScroll*.38);
        vec2 water = uv*vec2(uAspect,1.);
        c+=sheet(water + uPointer*.037 + vec2(0.,drift*.065), uTime*.34 + uScroll*.21, 1.08);
        c+=sheet(vec2(water.x*.74+.85,1.-water.y) + uPointer*.016 - vec2(0.,drift*.032),
                 uTime*.21 + uScroll*.12 + 1.28, .66);
        c+=vec3(.60,.69,.98)*dust(far,71.,.965)*.67;
        c+=vec3(.42,.54,.94)*dust(near,39.,.958)*.8;
        vec2 grid=far*27.;
        float marks=lineAt(grid.x)*lineAt(grid.y);
        c+=vec3(.12,.18,.34)*marks;
        float grain=hash(gl_FragCoord.xy)-.5;
        float alpha=clamp(max(max(c.r,c.g),c.b),0.,.52);
        gl_FragColor=vec4((c+grain*.006)/max(alpha,.001),alpha);
      }
    `
  }));
  background.frustumCulled=false;background.renderOrder=-10;scene.add(background);

  // Tessellate each route so the ripple and its travelling highlight share the same curved path.
  const bus=new T.Group();scene.add(bus);
  const positions=[],distances=[],lanes=[];
  function trace(points, lane) {
    const curve = new T.CatmullRomCurve3(points.map(([x,y]) => new T.Vector3(x,y,-2.8)));
    const samples = curve.getSpacedPoints(100);
    let length = 0;
    for(let i=1;i<samples.length;i++){
      const start=samples[i-1],end=samples[i];
      const next=length+start.distanceTo(end);
      positions.push(start.x,start.y,start.z,end.x,end.y,end.z);
      distances.push(length,next);lanes.push(lane,lane);length=next;
    }
  }
  for(let sector=0;sector<6;sector++){
    const angle=sector*Math.PI/3+.17;
    const c=Math.cos(angle),s=Math.sin(angle);
    for(let j=0;j<7;j++){
      const gap=(j-3)*.095;
      const path=[[2.72,gap],[3.23+gap,gap],[4.48+gap,1.25+gap],[6.3+gap,1.25+gap],[7.65+gap,-.10+gap],[19,-.10+gap]];
      trace(path.map(([x,y])=>[x*c-y*s,x*s+y*c]),sector*7+j);
    }
  }
  const geometry=new T.BufferGeometry();
  geometry.setAttribute('position',new T.Float32BufferAttribute(positions,3));
  geometry.setAttribute('travel',new T.Float32BufferAttribute(distances,1));
  geometry.setAttribute('lane',new T.Float32BufferAttribute(lanes,1));
  const traces=new T.LineSegments(geometry,new T.ShaderMaterial({
    uniforms,transparent:true,depthWrite:false,blending:T.AdditiveBlending,
    vertexShader:`
      attribute float travel,lane;
      uniform float uTime,uScroll;
      varying float vTravel,vLane;
      void main(){
        vTravel=travel;vLane=lane;
        vec3 point=position;
        float angle=floor(lane/7.)*1.04719755+.17;
        vec2 normal=vec2(-sin(angle),cos(angle));
        float phase=travel*1.13-uTime*.63+angle+mod(lane,7.)*.085+uScroll*.23;
        point.xy+=normal*smoothstep(0.,1.6,travel)*(.25*sin(phase)+.075*sin(phase*1.72+angle));
        gl_Position=projectionMatrix*modelViewMatrix*vec4(point,1.);
      }
    `,
    fragmentShader:`
      varying float vTravel,vLane;uniform float uTime,uScroll;
      void main(){
        float timing=vTravel*(.22+.018*mod(vLane,4.))-uTime*(.22+.019*mod(vLane,5.))+vLane*.117-uScroll*.25;
        float pulse=pow(max(0.,cos(timing*6.283185)),35.);
        vec3 base=mix(vec3(.28,.36,.69),vec3(.55,.69,1.),pulse);
        gl_FragColor=vec4(base,.13+pulse*.63);
      }
    `
  }));bus.add(traces);

  return {
    resize(aspect,focus) {
      uniforms.uAspect.value=aspect;uniforms.uFocus.value.copy(focus);
    },
    frame(t,journey,x,y,anchorX,anchorY) {
      uniforms.uTime.value=t;uniforms.uScroll.value=journey;uniforms.uPointer.value.set(x,y);
      bus.position.set(anchorX*1.14+x*.16,anchorY*1.12+y*.15+Math.sin(journey*.7)*.28,-.1);
      bus.rotation.set(.21+Math.sin(journey*.35)*.12+y*.035,-.14+x*.04,.11+Math.sin(journey*.57)*.18+Math.sin(t*.046)*.05);
    }
  };
}
