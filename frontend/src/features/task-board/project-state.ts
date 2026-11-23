import {createContext,useContext} from 'react'
export const AuthContext=createContext<{user:{id:string}}|null>(null)
export const ProjectRevision=createContext(0)
export const useProjectRevision=(_domain:string)=>useContext(ProjectRevision)
